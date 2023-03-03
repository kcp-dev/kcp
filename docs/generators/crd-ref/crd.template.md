---
title: {{ .Title }}
description: |
{{- if .Description }}
{{ .Description | indent 2 }}
{{- else }}
  Custom resource definition (CRD) schema reference page for the {{ .Title }} 
  resource ({{ .NamePlural }}.{{ .Group }}), as part of the Giant Swarm 
  Management API documentation.
{{- end }}
weight: {{ .Weight }}
---

{{- with .Metadata.Deprecation }}
{{ "{{" }}% pageinfo color="warning" %{{ "}}"}}
{{- with .Info }}
{{ . }}
{{- end }}
{{- with .ReplacedBy }}
This CRD is being replaced by <a href="../{{ .FullName }}/">{{ .ShortName }}</a>.
{{- end }}
{{"{{% /pageinfo %}}"}}
{{- end }}

## {{ .Title }} CRD schema reference (group {{ .Group }})
<div class="lead">{{`{{`}} page.meta.description {{`}}`}}</div>

<dl class="crd-meta">
<dt class="fullname">Full name:</dt>
<dd class="fullname">{{ .NamePlural }}.{{ .Group }}</dd>
<dt class="groupname">Group:</dt>
<dd class="groupname">{{ .Group }}</dd>
<dt class="singularname">Singular name:</dt>
<dd class="singularname">{{ .NameSingular }}</dd>
<dt class="pluralname">Plural name:</dt>
<dd class="pluralname">{{ .NamePlural }}</dd>
<dt class="scope">Scope:</dt>
<dd class="scope">{{ .Scope }}</dd>
<dt class="versions">Versions:</dt>
<dd class="versions">
{{- range .Versions -}}
<a class="version" href="#{{.}}" title="Show schema for version {{.}}">{{.}}</a>
{{- end -}}
</dd>
</dl>

{{ if .VersionSchemas }}
{{ $versionSchemas := .VersionSchemas }}
{{ range .Versions -}}
{{ $versionName := . -}}
{{ $versionSchema := (index $versionSchemas $versionName) -}}
<div class="crd-schema-version">
<h2 id="{{$versionName}}">Version {{$versionName}}</h2>

{{ with $versionSchema.ExampleCR }}
<h3 id="crd-example-{{$versionName}}">Example CR</h3>

```yaml
{{ .|raw -}}
```

{{end}}

<h3 id="property-details-{{$versionName}}">Properties</h3>

{{ range $versionSchema.Properties }}
<div class="property depth-{{.Depth}}">
<div class="property-header">
<h3 class="property-path" id="{{$versionName}}-{{.Path}}">{{.Path}}</h3>
</div>
<div class="property-body">
<div class="property-meta">
{{with .Type}}<span class="property-type">{{.}}</span>{{end}}
{{ if not .Required }}
{{ else -}}
<span class="property-required">Required</span>
{{ end -}}
</div>
{{with .Description}}
<div class="property-description">
{{.|markdown}}
</div>
{{end}}
</div>
</div>
{{ end }}

{{ if $versionSchema.Annotations }}
<h3 id="annotation-details-{{$versionName}}">Annotations</h3>

{{ range $versionSchema.Annotations }}
<div class="annotation">
<div class="annotation-header">
<h3 class="annotation-path" id="{{.CRDVersion}}-{{.Annotation}}">{{.Annotation}}</h3>
</div>
<div class="annotation-body">
<div class="annotation-meta">
{{with .Release}}<span class="annotation-release">{{.}}</span>{{end}}
</div>
{{with .Documentation}}
<div class="annotation-description">
{{.|markdown}}
</div>
{{end}}
</div>
</div>
{{ end }}
{{ end }}

</div>
{{end}}

{{ else }}
<div class="crd-noversions">
<p>We currently cannot show any schema information on this <abbr title="custom resource definition">CRD</abbr>. Sorry for the inconvenience!</p>
<p>Please refer to the <a href="https://pkg.go.dev/github.com/giantswarm/apiextensions/pkg/apis/">Godoc</a> or <a href="https://github.com/giantswarm/apiextensions/tree/master/pkg/apis">source</a> for details.</p>
</div>
{{ end }}
