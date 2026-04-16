{{/*
Expand the name of the chart.
*/}}
{{- define "binarylane-controllers.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "binarylane-controllers.fullname" -}}
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
Extract port number from a listenAddr like "0.0.0.0:8086"
*/}}
{{- define "binarylane-controllers.port" -}}
{{- splitList ":" . | last | int -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "binarylane-controllers.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "binarylane-controllers.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "binarylane-controllers.selectorLabels" -}}
app.kubernetes.io/name: {{ include "binarylane-controllers.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account name
*/}}
{{- define "binarylane-controllers.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "binarylane-controllers.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Secret name for API token
*/}}
{{- define "binarylane-controllers.secretName" -}}
{{- if .Values.existingSecret }}
{{- .Values.existingSecret }}
{{- else }}
{{- include "binarylane-controllers.fullname" . }}
{{- end }}
{{- end }}
