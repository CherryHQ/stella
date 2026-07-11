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
Image reference (repository:tag, tag defaults to appVersion).
*/}}
{{- define "stella.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
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

{{- if not (trim $v.baseURL) -}}
{{- fail "stella: baseURL is required — set it to the externally reachable URL clients use (STELLA_BASE_URL), e.g. --set baseURL=https://stella.example.com" -}}
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
{{- end -}}
