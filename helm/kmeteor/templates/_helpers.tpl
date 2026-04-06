{{/*
Expand the name of the chart.
*/}}
{{- define "kmeteor.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Full name: release + chart, capped at 63 chars.
*/}}
{{- define "kmeteor.fullname" -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels applied to every resource.
*/}}
{{- define "kmeteor.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
app.kubernetes.io/name: {{ include "kmeteor.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels used by the Deployment and its Pods.
*/}}
{{- define "kmeteor.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kmeteor.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
