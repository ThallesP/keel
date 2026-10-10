package app

import (
	"cmp"
	_ "embed"
	"strings"
)

//go:embed tracing_prompt.md
var agentPromptTemplate string

func agentPrompt(service, project string) string {
	inProject := ""
	if project != "" {
		inProject = " in the project `" + project + "`"
	}
	where := "It runs on Keel" + inProject + "; find which service it is with `keel service list` and use that name wherever this says `<service>`."
	if service != "" {
		where = "It runs on Keel as the service `" + service + "`" + inProject + "."
	}
	return strings.NewReplacer("{{WHERE}}", where, "{{SVC}}", cmp.Or(service, "<service>")).Replace(agentPromptTemplate)
}
