{{/*
Chart name, optionally overridden.
*/}}
{{- define "stella.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully qualified app name.
*/}}
{{- define "stella.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "stella.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels.
*/}}
{{- define "stella.labels" -}}
helm.sh/chart: {{ include "stella.chart" . }}
{{ include "stella.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels.
*/}}
{{- define "stella.selectorLabels" -}}
app.kubernetes.io/name: {{ include "stella.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
ServiceAccount name.
*/}}
{{- define "stella.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "stella.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Image reference. A digest pins the image immutably and wins over any tag;
otherwise repository:tag, with tag defaulting to the chart's appVersion.
*/}}
{{- define "stella.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}
{{- end -}}

{{/*
Name of the PVC backing STELLA_HOME.
*/}}
{{- define "stella.pvcName" -}}
{{- if .Values.persistence.existingClaim -}}
{{- .Values.persistence.existingClaim -}}
{{- else -}}
{{- printf "%s-data" (include "stella.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
validate — hard configuration gates. Each rule fails rendering with an
actionable message. Kept in one place so every template shares the same checks.
*/}}
{{- define "stella.validate" -}}
{{- $v := .Values -}}

{{- if ne (int $v.replicaCount) 1 -}}
{{- fail (printf "stella: replicaCount must be 1 (got %v). Stella does not support multiple replicas; see the 'Why only one replica?' section of the Kubernetes deployment docs. The Deployment strategy is fixed to Recreate." $v.replicaCount) -}}
{{- end -}}

{{- $baseURL := trim $v.baseURL -}}
{{- if not $baseURL -}}
{{- fail "stella: baseURL is required — set it to the externally reachable URL clients use (STELLA_BASE_URL), e.g. --set baseURL=https://stella.example.com" -}}
{{- end -}}
{{- if not (or (hasPrefix "http://" $baseURL) (hasPrefix "https://" $baseURL)) -}}
{{- fail (printf "stella: baseURL must include an http:// or https:// scheme (got %q). It is the public address clients reach, used for OAuth callbacks and channel deep links." $baseURL) -}}
{{- end -}}
{{- $authority := regexReplaceAll "^https?://" $baseURL "" -}}
{{- $host := regexReplaceAll "^([^/:]+).*$" $authority "${1}" -}}
{{- /* IPv6 hosts are bracketed ([::1], [::1]:8443); the host regex above cannot
       capture them, so match the bracketed loopback on the raw authority. */ -}}
{{- if or (eq $host "localhost") (hasPrefix "127." $host) (eq $host "0.0.0.0") (hasPrefix "[::1]" $authority) (hasPrefix "[0:0:0:0:0:0:0:1]" $authority) -}}
{{- fail (printf "stella: baseURL %q points at a loopback/bind address, but it must be the externally reachable URL clients use (the ingress address), or OAuth callbacks and channel links break." $baseURL) -}}
{{- end -}}

{{- if not (trim $v.secrets.existingSecret) -}}
{{- fail "stella: secrets.existingSecret is required — create a Secret with STELLA_VAULT_KEY and STELLA_DATABASE_URL, then set secrets.existingSecret to its name. This chart does not create the Secret." -}}
{{- end -}}

{{- $backend := trim $v.sandbox.backend -}}
{{- if not $backend -}}
{{- fail "stella: sandbox.backend is required — set it to 'local' (bubblewrap, experimental) or 'none' (no isolation). The 'docker' backend is not supported by this chart." -}}
{{- end -}}
{{- if not (has $backend (list "local" "none")) -}}
{{- fail (printf "stella: sandbox.backend must be 'local' or 'none' (got %q). The 'docker' backend is not supported by this chart." $backend) -}}
{{- end -}}
{{- if and (eq $backend "none") (not $v.sandbox.allowUnsafeHostExecution) -}}
{{- fail "stella: sandbox.backend=none runs agent tools directly inside the Stella pod with no isolation. Set sandbox.allowUnsafeHostExecution=true to acknowledge this, or choose sandbox.backend=local." -}}
{{- end -}}

{{- $s := $v.shutdown -}}
{{- $minGrace := add (int $s.preStopSeconds) (int $s.httpSeconds) (int $s.riverSoftStopSeconds) 10 -}}
{{- if lt (int $s.terminationGracePeriodSeconds) $minGrace -}}
{{- fail (printf "stella: shutdown.terminationGracePeriodSeconds (%d) is too small. It must be >= preStopSeconds + httpSeconds + riverSoftStopSeconds + 10 (cleanup margin) = %d + %d + %d + 10 = %d, or the kubelet SIGKILLs the pod mid-drain." (int $s.terminationGracePeriodSeconds) (int $s.preStopSeconds) (int $s.httpSeconds) (int $s.riverSoftStopSeconds) $minGrace) -}}
{{- end -}}

{{- if and (not $v.persistence.enabled) (not $v.persistence.allowEphemeralDataLoss) -}}
{{- fail "stella: persistence.enabled=false stores STELLA_HOME (user workspaces, attachments, article files) in an emptyDir that is lost on every pod restart. Set persistence.allowEphemeralDataLoss=true to acknowledge this, or keep persistence enabled." -}}
{{- end -}}

{{- range $v.extraEnv -}}
{{- if has .name (list "STELLA_BASE_URL" "STELLA_SANDBOX_BACKEND" "STELLA_HTTP_SHUTDOWN_TIMEOUT" "STELLA_RIVER_SOFT_STOP_TIMEOUT" "STELLA_VAULT_KEY" "STELLA_DATABASE_URL" "HOST" "PORT" "STELLA_REQUIRE_EXTERNAL_DB") -}}
{{- fail (printf "stella: extraEnv must not set %s — it is managed by the chart's typed values (baseURL, secrets.*, sandbox.*, shutdown.*), which enforce this chart's safety contract. Setting it twice would make the effective value ambiguous." .name) -}}
{{- end -}}
{{- end -}}

{{- range $k, $val := $v.podLabels -}}
{{- if has $k (list "app.kubernetes.io/name" "app.kubernetes.io/instance") -}}
{{- fail (printf "stella: podLabels must not set %s — it is the Deployment's immutable selector label. Overriding it would leave the Deployment unable to find its own pods." $k) -}}
{{- end -}}
{{- end -}}

{{- if $v.ingress.enabled -}}
{{- if not $v.ingress.hosts -}}
{{- fail "stella: ingress.enabled=true requires at least one entry under ingress.hosts (each with a host and one or more paths), or the rendered Ingress is rejected by the API server." -}}
{{- end -}}
{{- range $v.ingress.hosts -}}
{{- if or (not .host) (not .paths) -}}
{{- fail "stella: every ingress.hosts entry needs a non-empty host and at least one path." -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
