{{/*
Expand the name of the chart.
*/}}
{{- define "kcp-tmc.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "kcp-tmc.fullname" -}}
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
{{- define "kcp-tmc.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kcp-tmc.labels" -}}
helm.sh/chart: {{ include "kcp-tmc.chart" . }}
{{ include "kcp-tmc.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- with .Values.global.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "kcp-tmc.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kcp-tmc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "kcp-tmc.serviceAccountName" -}}
{{- if .Values.rbac.serviceAccount.create }}
{{- default (include "kcp-tmc.fullname" .) .Values.rbac.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.rbac.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the image name
*/}}
{{- define "kcp-tmc.image" -}}
{{- $registry := .Values.global.imageRegistry | default .Values.kcp.image.registry -}}
{{- $repository := .Values.kcp.image.repository -}}
{{- $tag := .Values.kcp.image.tag | default .Chart.AppVersion -}}
{{- if $registry }}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- else }}
{{- printf "%s:%s" $repository $tag -}}
{{- end }}
{{- end }}

{{/*
TMC component configuration
*/}}
{{- define "kcp-tmc.tmcConfig" -}}
tmc:
  enabled: {{ .Values.kcp.tmc.enabled }}
  errorHandling:
    enabled: {{ .Values.kcp.tmc.errorHandling.enabled }}
  healthMonitoring:
    enabled: {{ .Values.kcp.tmc.healthMonitoring.enabled }}
  metrics:
    enabled: {{ .Values.kcp.tmc.metrics.enabled }}
    port: {{ .Values.kcp.tmc.metrics.port }}
  recovery:
    enabled: {{ .Values.kcp.tmc.recovery.enabled }}
  virtualWorkspaces:
    enabled: {{ .Values.kcp.tmc.virtualWorkspaces.enabled }}
  placementController:
    enabled: {{ .Values.kcp.tmc.placementController.enabled }}
{{- end }}

{{/*
Generate certificates for KCP
*/}}
{{- define "kcp-tmc.gen-certs" -}}
{{- $cn := printf "%s.%s.svc.cluster.local" (include "kcp-tmc.fullname" .) .Release.Namespace -}}
{{- $altNames := list $cn -}}
{{- $ca := genCA (printf "%s-ca" (include "kcp-tmc.fullname" .)) 365 -}}
{{- $cert := genSignedCert $cn nil $altNames 365 $ca -}}
ca.crt: {{ $ca.Cert | b64enc }}
tls.crt: {{ $cert.Cert | b64enc }}
tls.key: {{ $cert.Key | b64enc }}
{{- end }}

{{/*
TMC feature flags environment variables
*/}}
{{- define "kcp-tmc.featureEnv" -}}
{{- if .Values.kcp.tmc.enabled }}
- name: KCP_TMC_ENABLED
  value: "true"
{{- if .Values.kcp.tmc.errorHandling.enabled }}
- name: KCP_TMC_ERROR_HANDLING
  value: "true"
{{- end }}
{{- if .Values.kcp.tmc.healthMonitoring.enabled }}
- name: KCP_TMC_HEALTH_MONITORING
  value: "true"
{{- end }}
{{- if .Values.kcp.tmc.metrics.enabled }}
- name: KCP_TMC_METRICS
  value: "true"
{{- end }}
{{- if .Values.kcp.tmc.recovery.enabled }}
- name: KCP_TMC_RECOVERY
  value: "true"
{{- end }}
{{- if .Values.kcp.tmc.virtualWorkspaces.enabled }}
- name: KCP_TMC_VIRTUAL_WORKSPACES
  value: "true"
{{- end }}
{{- if .Values.kcp.tmc.placementController.enabled }}
- name: KCP_TMC_PLACEMENT_CONTROLLER
  value: "true"
{{- end }}
{{- end }}
{{- end }}