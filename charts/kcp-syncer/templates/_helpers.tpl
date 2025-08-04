{{/*
Expand the name of the chart.
*/}}
{{- define "kcp-syncer.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "kcp-syncer.fullname" -}}
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

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "kcp-syncer.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kcp-syncer.labels" -}}
helm.sh/chart: {{ include "kcp-syncer.chart" . }}
{{ include "kcp-syncer.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "kcp-syncer.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kcp-syncer.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "kcp-syncer.serviceAccountName" -}}
{{- if .Values.rbac.serviceAccount.create }}
{{- default (include "kcp-syncer.fullname" .) .Values.rbac.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.rbac.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the image name
*/}}
{{- define "kcp-syncer.image" -}}
{{- $registry := .Values.global.imageRegistry | default .Values.syncer.image.registry -}}
{{- $repository := .Values.syncer.image.repository -}}
{{- $tag := .Values.syncer.image.tag | default .Chart.AppVersion -}}
{{- if $registry }}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- else }}
{{- printf "%s:%s" $repository $tag -}}
{{- end }}
{{- end }}

{{/*
Generate sync target UID
*/}}
{{- define "kcp-syncer.syncTargetUID" -}}
{{- if .Values.syncer.syncTarget.uid }}
{{- .Values.syncer.syncTarget.uid }}
{{- else }}
{{- uuidv4 }}
{{- end }}
{{- end }}

{{/*
Validate required values
*/}}
{{- define "kcp-syncer.validateConfig" -}}
{{- if not .Values.syncer.syncTarget.name }}
{{- fail "syncer.syncTarget.name is required" }}
{{- end }}
{{- if not .Values.syncer.kcp.endpoint }}
{{- fail "syncer.kcp.endpoint is required" }}
{{- end }}
{{- end }}