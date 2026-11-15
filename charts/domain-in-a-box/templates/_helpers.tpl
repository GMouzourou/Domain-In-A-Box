{{/*
Expand the name of the chart.
*/}}
{{- define "domain-in-a-box.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "domain-in-a-box.fullname" -}}
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

{{/*
Common labels
*/}}
{{- define "domain-in-a-box.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "domain-in-a-box.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels
*/}}
{{- define "domain-in-a-box.selectorLabels" -}}
app.kubernetes.io/name: {{ include "domain-in-a-box.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Role-derived settings. Unset values follow the controller role, while explicit
values remain overrides so that contradictory configuration still fails.
*/}}
{{- define "domain-in-a-box.isPrimary" -}}
{{- ternary "true" "" (eq .Values.domain.controllerMode "primary") -}}
{{- end -}}

{{- define "domain-in-a-box.netbiosName" -}}
{{- default (first (splitList "." .Values.domain.realm) | upper | trunc 15) .Values.domain.name -}}
{{- end -}}

{{- define "domain-in-a-box.dhcpEnabled" -}}
{{- if hasKey .Values.domain "dhcpEnabled" -}}
{{- ternary "true" "" (eq (toString .Values.domain.dhcpEnabled) "true") -}}
{{- else -}}
{{- include "domain-in-a-box.isPrimary" . -}}
{{- end -}}
{{- end -}}

{{- define "domain-in-a-box.storkServerEnabled" -}}
{{- if hasKey .Values.storkServer "enabled" -}}
{{- ternary "true" "" (eq (toString .Values.storkServer.enabled) "true") -}}
{{- else -}}
{{- include "domain-in-a-box.isPrimary" . -}}
{{- end -}}
{{- end -}}

{{/*
Resource name of the release that runs the Stork server: this release for a
primary, or peer.name for a secondary.
*/}}
{{- define "domain-in-a-box.storkOwner" -}}
{{- if include "domain-in-a-box.isPrimary" . -}}
{{- include "domain-in-a-box.fullname" . -}}
{{- else -}}
{{- .Values.peer.name -}}
{{- end -}}
{{- end -}}

{{- define "domain-in-a-box.networkName" -}}
{{- default (printf "%s-macvlan" (include "domain-in-a-box.fullname" .)) .Values.multus.networkName -}}
{{- end -}}

{{/*
An unset name derives from the release; an explicit empty string disables
controller TLS.
*/}}
{{- define "domain-in-a-box.controllerTLSSecret" -}}
{{- if hasKey .Values.tls.controller "secretName" -}}
{{- .Values.tls.controller.secretName -}}
{{- else -}}
{{- printf "%s-controller-tls" (include "domain-in-a-box.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "domain-in-a-box.storkServerTLSSecret" -}}
{{- $owner := include "domain-in-a-box.storkOwner" . -}}
{{- default (ternary (printf "%s-stork-server-tls" $owner) "" (ne $owner "")) .Values.tls.storkServer.secretName -}}
{{- end -}}

{{- define "domain-in-a-box.storkServerHost" -}}
{{- $owner := include "domain-in-a-box.storkOwner" . -}}
{{- $clusterDomain := default "cluster.local" .Values.clusterDNS.domain -}}
{{- default (ternary (printf "%s-stork-server.%s.svc.%s" $owner .Release.Namespace $clusterDomain) "" (ne $owner "")) .Values.storkServer.host -}}
{{- end -}}
