{{- define "dns-bridge.fullname" -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "dns-bridge.rbacName" -}}
{{- printf "%s-%s" (include "dns-bridge.fullname" . | trunc 54 | trimSuffix "-") (sha256sum .Release.Namespace | trunc 8) -}}
{{- end -}}
{{- define "dns-bridge.zone" -}}
{{- default .Values.realm .Values.zone | lower | trimSuffix "." -}}
{{- end -}}
{{- define "dns-bridge.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
{{- define "dns-bridge.labels" -}}
{{ include "dns-bridge.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
{{- end -}}
