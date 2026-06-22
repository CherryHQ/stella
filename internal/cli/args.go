package cli

import (
	"slices"
	"strings"

	ucli "github.com/urfave/cli/v2"
)

// HoistFlags reorders argv so flags may appear after positional arguments.
//
// urfave/cli v2 parses each command's args with the stdlib flag package, which
// stops at the first non-flag token: `stella goal get <id> --json` leaves
// `--json` unparsed and silently falls back to text output. Users (and agents)
// reasonably expect flags to work in any position, so this walks the command
// tree, finds the leaf command's argument tail, and moves its flags (with their
// values) ahead of the positionals before handing argv to the app.
//
// It is value-aware: a flag that takes a value carries the following token with
// it, so `--title my goal` is not split. A `--` terminator stops reordering, so
// intentional positional-looking arguments can still be passed verbatim.
func HoistFlags(app *ucli.App, argv []string) []string {
	if app == nil || len(argv) < 2 {
		return argv
	}

	out := make([]string, 0, len(argv))
	out = append(out, argv[0])
	rest := argv[1:]

	// Phase 1: walk the command path, passing scope flags and command tokens
	// through untouched. Stops at the first token that is neither a flag nor a
	// known subcommand — that token begins the leaf command's argument tail.
	flags := app.Flags
	cmds := app.Commands
	i := 0
	for i < len(rest) {
		tok := rest[i]
		if tok == "--" {
			break
		}
		if isFlag(tok) {
			out = append(out, tok)
			i++
			if takesValue(flags, tok) && !strings.Contains(tok, "=") && i < len(rest) {
				out = append(out, rest[i])
				i++
			}
			continue
		}
		sub := findCommand(cmds, tok)
		if sub == nil {
			break
		}
		out = append(out, tok)
		i++
		flags = sub.Flags
		cmds = sub.Subcommands
	}

	// Phase 2: in the leaf tail, emit flags (with values) first, positionals
	// after. Everything past a `--` terminator is left untouched as positional.
	tail := rest[i:]
	var hoisted, positional []string
	terminated := false
	for j := 0; j < len(tail); j++ {
		tok := tail[j]
		if terminated {
			positional = append(positional, tok)
			continue
		}
		if tok == "--" {
			terminated = true
			positional = append(positional, tok)
			continue
		}
		if isFlag(tok) {
			hoisted = append(hoisted, tok)
			if takesValue(flags, tok) && !strings.Contains(tok, "=") && j+1 < len(tail) {
				j++
				hoisted = append(hoisted, tail[j])
			}
			continue
		}
		positional = append(positional, tok)
	}
	out = append(out, hoisted...)
	out = append(out, positional...)
	return out
}

// isFlag reports whether tok is a flag token (`-x` / `--name`), not a bare
// positional or the `-`/`--` sentinels.
func isFlag(tok string) bool {
	return len(tok) > 1 && tok[0] == '-' && tok != "--"
}

// findCommand resolves a command by name or alias within a scope.
func findCommand(cmds []*ucli.Command, name string) *ucli.Command {
	for _, c := range cmds {
		if c.HasName(name) {
			return c
		}
	}
	return nil
}

// takesValue reports whether the flag named by tok consumes a following token as
// its value. Only bool flags are valueless; an unknown flag is treated as
// valueless so an unrecognized token never swallows the argument after it.
func takesValue(flags []ucli.Flag, tok string) bool {
	name := strings.TrimLeft(tok, "-")
	if eq := strings.IndexByte(name, '='); eq >= 0 {
		name = name[:eq]
	}
	for _, f := range flags {
		if !flagHasName(f, name) {
			continue
		}
		_, isBool := f.(*ucli.BoolFlag)
		return !isBool
	}
	return false
}

// flagHasName reports whether flag f is registered under name (long or short).
func flagHasName(f ucli.Flag, name string) bool {
	return slices.Contains(f.Names(), name)
}
