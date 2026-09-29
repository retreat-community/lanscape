{{- define "lanscape.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "lanscape.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "lanscape.selector" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "lanscape.tag" -}}
{{- default .Chart.AppVersion .Values.image.tag -}}
{{- end -}}

{{- define "lanscape.gateway" -}}
{{- if .Values.agent.server -}}
{{- .Values.agent.server -}}
{{- else -}}
{{- printf "%s.%s.svc:8443" (include "lanscape.fullname" .) .Release.Namespace -}}
{{- end -}}
{{- end -}}

{{- define "lanscape.tokenSecret" -}}
{{- default (printf "%s-agent" (include "lanscape.fullname" .)) .Values.agent.existingTokenSecret -}}
{{- end -}}
