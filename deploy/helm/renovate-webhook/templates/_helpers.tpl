{{/*
Expand the name of the chart.
*/}}
{{- define "renovate-webhook.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name, capped at the 63 character label limit.
*/}}
{{- define "renovate-webhook.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "renovate-webhook.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "renovate-webhook.labels" -}}
helm.sh/chart: {{ include "renovate-webhook.chart" . }}
{{ include "renovate-webhook.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "renovate-webhook.selectorLabels" -}}
app.kubernetes.io/name: {{ include "renovate-webhook.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "renovate-webhook.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "renovate-webhook.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the secret holding the webhook secret and the GitHub App private key.
*/}}
{{- define "renovate-webhook.secretName" -}}
{{- if .Values.secret.existingSecret }}
{{- .Values.secret.existingSecret }}
{{- else }}
{{- include "renovate-webhook.fullname" . }}
{{- end }}
{{- end }}

{{/*
Fail early on configuration the service cannot start without, so the mistake
surfaces at install time rather than in a CrashLoopBackOff.
*/}}
{{- define "renovate-webhook.validate" -}}
{{- if not .Values.config.runnerRepository }}
{{- fail "config.runnerRepository is required: set it to the owner/repo holding the Renovate runner workflow" }}
{{- end }}
{{- if not (regexMatch "^[^/]+/[^/]+$" .Values.config.runnerRepository) }}
{{- fail (printf "config.runnerRepository must be in owner/repo form, got %q" .Values.config.runnerRepository) }}
{{- end }}
{{- if not .Values.secret.existingSecret }}
{{- if not .Values.secret.create }}
{{- fail "set secret.existingSecret, or secret.create with secret.webhookSecret" }}
{{- end }}
{{- if not .Values.secret.webhookSecret }}
{{- fail "secret.webhookSecret is required unless secret.existingSecret is set" }}
{{- end }}
{{- if and (not .Values.secret.githubAppPrivateKey) (not .Values.config.dryRun) }}
{{- fail "secret.githubAppPrivateKey is required unless config.dryRun is true or secret.existingSecret is set" }}
{{- end }}
{{- end }}
{{- if and (not .Values.config.githubAppId) (not .Values.config.dryRun) }}
{{- fail "config.githubAppId is required unless config.dryRun is true" }}
{{- end }}
{{- end }}
