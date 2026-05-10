package tui

import "strings"

// slashCommand 是一个斜杠命令描述。
type slashCommand struct {
	Name  string // 含前导 /，如 "/help"
	Desc  string
	Usage string
}

var slashCommands = []slashCommand{
	{Name: "/help", Desc: "show available commands", Usage: "/help"},
	{Name: "/config", Desc: "configure provider or render theme", Usage: "/config [set key value]"},
	{Name: "/context", Desc: "show prompt context breakdown", Usage: "/context"},
	{Name: "/model", Desc: "show current provider and model", Usage: "/model"},
	{Name: "/tools", Desc: "list registered tools", Usage: "/tools"},
	{Name: "/skill", Desc: "list loaded skills", Usage: "/skill"},
	{Name: "/sessions", Desc: "list all sessions", Usage: "/sessions"},
	{Name: "/session", Desc: "switch to a session", Usage: "/session <id>"},
	{Name: "/clear", Desc: "reset current session history", Usage: "/clear"},
	{Name: "/rollback", Desc: "rollback files to a previous turn", Usage: "/rollback [list|<turn-id>]"},
	{Name: "/export", Desc: "export conversation", Usage: "/export [json|md] [path]"},
	{Name: "/theme", Desc: "set markdown theme", Usage: "/theme [auto|dark|light|dracula|ascii|notty]"},
	{Name: "/status", Desc: "show token and context stats", Usage: "/status"},
	{Name: "/exit", Desc: "exit the agent", Usage: "/exit"},
}

// filterCommands 返回匹配前缀的命令列表。
func filterCommands(prefix string) []slashCommand {
	prefix = strings.ToLower(prefix)
	if prefix == "" || prefix == "/" {
		return slashCommands
	}
	var out []slashCommand
	for _, c := range slashCommands {
		if strings.HasPrefix(strings.ToLower(c.Name), prefix) {
			out = append(out, c)
		}
	}
	return out
}
