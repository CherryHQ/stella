package kubernetes

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"sync"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"

	sandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// Unbuffered reads preserve every byte after the metadata frame for the child.
// Credentials travel only over the exec stdin channel, never in URLs or PodSpec.
const launcher = `import os,sys,json,struct

def exact(n):
 b=b''
 while len(b)<n:
  v=os.read(0,n-len(b))
  if not v: raise RuntimeError('short execution frame')
  b+=v
 return b
n=struct.unpack('!I',exact(4))[0]
if n>1048576: raise RuntimeError('execution frame too large')
m=json.loads(exact(n))
os.chdir(m['cwd'])
os.execvpe(m['argv'][0],m['argv'],m['env'])
`

func (c *Client) stream(ctx context.Context, p *core.Pod, argv []string, in io.Reader, out, stderr io.Writer) error {
	current, err := c.api.CoreV1().Pods(c.owner.Namespace).Get(ctx, p.Name, meta.GetOptions{})
	if err != nil {
		return err
	}
	if current.UID != p.UID || current.DeletionTimestamp != nil {
		return errors.New("kubernetes: execution generation is no longer current")
	}
	rc := rest.CopyConfig(c.rest)
	rc.GroupVersion = &core.SchemeGroupVersion
	rc.APIPath = "/api"
	rc.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	client, err := rest.RESTClientFor(rc)
	if err != nil {
		return err
	}
	url := client.Post().Resource("pods").Namespace(c.owner.Namespace).Name(p.Name).SubResource("exec").VersionedParams(&core.PodExecOptions{Container: "sandbox", Command: argv, Stdin: in != nil, Stdout: out != nil, Stderr: stderr != nil}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.rest, "POST", url)
	if err != nil {
		return err
	}
	return executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: in, Stdout: out, Stderr: stderr})
}

func (s *session) frame(req sandbox.ProcessRequest) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.invalid {
		return nil, errors.New("kubernetes: execution generation is invalid")
	}
	if err := s.resolver.ValidateBackingPaths(); err != nil {
		return nil, err
	}
	cwd := req.Cwd
	if cwd == "" {
		cwd = s.policy.Filesystem.WorkingDir
	}
	resolved, err := s.resolver.ResolveDirectory(cwd)
	if err != nil {
		return nil, err
	}
	env := s.environment(req.Env, req.EnvMode)
	if s.policy.NetworkModeOrDefault() == sandbox.NetworkDisabled {
		delete(env, "STELLA_SERVER_URL")
	}
	payload, err := json.Marshal(struct {
		Argv []string          `json:"argv"`
		Cwd  string            `json:"cwd"`
		Env  map[string]string `json:"env"`
	}{append([]string{req.Path}, req.Args...), resolved.SandboxPath, env})
	if err != nil {
		return nil, err
	}
	if len(payload) > 1<<20 {
		return nil, errors.New("kubernetes: execution metadata exceeds 1 MiB")
	}
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(payload))), payload...), nil
}

func (s *session) run(ctx context.Context, frame []byte, in io.Reader, out, stderr io.Writer) (sandbox.ExecResult, error) {
	input := io.Reader(bytes.NewReader(frame))
	if in != nil {
		input = io.MultiReader(input, in)
	}
	err := s.client.stream(ctx, s.pod, []string{"/usr/bin/python3", "-I", "-c", launcher}, input, out, stderr)
	result := sandbox.ExecResult{}
	if err == nil {
		return result, nil
	}
	var exit utilexec.ExitError
	if errors.As(err, &exit) {
		result.ExitCode = exit.ExitStatus()
		return result, nil
	}
	result.ExitCode = -1
	result.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	// A broken stream has an unknown outcome. Fence the entire generation; never replay.
	return result, errors.Join(err, s.Close())
}

func (s *session) Exec(ctx context.Context, command string, opts sandbox.ExecOptions) (sandbox.ExecResult, error) {
	req := sandbox.ProcessRequest{Path: "/bin/bash", Args: []string{"-c", command}, Cwd: opts.Cwd, Env: opts.Env, EnvMode: opts.EnvMode}
	frame, err := s.frame(req)
	if err != nil {
		return sandbox.ExecResult{}, err
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = s.policy.Timeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	out, stderr := sandbox.NewExecOutputBuffer(), sandbox.NewExecOutputBuffer()
	result, err := s.run(ctx, frame, nil, out, stderr)
	result.Stdout = out.String()
	result.Stderr = stderr.String()
	return result, err
}

type process struct {
	session     *session
	in          *io.PipeWriter
	out, stderr *io.PipeReader
	done        chan struct{}
	result      sandbox.ExecResult
	err         error
	cancel      context.CancelFunc
	once        sync.Once
}

func (s *session) StartProcess(ctx context.Context, req sandbox.ProcessRequest) (sandbox.ProcessHandle, error) {
	frame, err := s.frame(req)
	if err != nil {
		return nil, err
	}
	var cancel context.CancelFunc
	timeout := req.Timeout
	if timeout == 0 {
		timeout = s.policy.Timeout
	}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	stdin, in := io.Pipe()
	out, stdout := io.Pipe()
	stderr, errout := io.Pipe()
	p := &process{session: s, in: in, out: out, stderr: stderr, done: make(chan struct{}), cancel: cancel}
	go func() {
		defer cancel()
		p.result, p.err = s.run(ctx, frame, stdin, stdout, errout)
		_ = stdin.Close()
		_ = stdout.Close()
		_ = errout.Close()
		close(p.done)
	}()
	return p, nil
}
func (p *process) PID() int              { return 0 }
func (p *process) Stdin() io.WriteCloser { return p.in }
func (p *process) Stdout() io.ReadCloser { return p.out }
func (p *process) Stderr() io.ReadCloser { return p.stderr }
func (p *process) Wait(ctx context.Context) (sandbox.ExecResult, error) {
	select {
	case <-p.done:
		return p.result, p.err
	case <-ctx.Done():
		return sandbox.ExecResult{}, ctx.Err()
	}
}

func (p *process) Close() error {
	p.once.Do(func() { p.cancel(); _ = p.in.Close(); _ = p.out.Close(); _ = p.stderr.Close() })
	select {
	case <-p.done:
		if p.result.ExitCode >= 0 {
			return nil
		}
		return p.session.Close()
	default:
		return p.session.Close()
	}
}
