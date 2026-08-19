{{ range .Versions -}}
{{ if .Tag.Previous -}}
**Full changelog:** [{{ .Tag.Previous.Name }}...{{ .Tag.Name }}]({{ $.Info.RepositoryURL }}/compare/{{ .Tag.Previous.Name }}...{{ .Tag.Name }})
{{ end -}}
{{ range .CommitGroups }}
### {{ .Title }}
{{ range .Commits }}
- {{ if .Scope }}**{{ .Scope }}:** {{ end }}{{ .Subject }} ([{{ .Hash.Short }}]({{ $.Info.RepositoryURL }}/commit/{{ .Hash.Long }})){{ end }}
{{ end -}}

{{- if .NoteGroups -}}
{{ range .NoteGroups }}
### {{ .Title }}
{{ range .Notes }}
{{ .Body }}
{{ end }}
{{ end -}}
{{ end -}}
{{ end -}}
