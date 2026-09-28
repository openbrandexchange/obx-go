package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
)

var shells = []string{"bash", "zsh", "fish", "powershell"}

func defineCompletion(e *env, fs *flag.FlagSet) func(context.Context, []string) (int, error) {
	return func(ctx context.Context, pos []string) (int, error) {
		if len(pos) != 1 {
			return exitFailed, usageError{e.t("completion.usage")}
		}
		specs := completionSpecs(e)
		switch pos[0] {
		case "bash":
			writeBash(e.stdout, specs)
		case "zsh":
			writeZsh(e.stdout, specs)
		case "fish":
			writeFish(e.stdout, specs)
		case "powershell", "pwsh":
			writePowerShell(e.stdout, specs)
		default:
			return exitFailed, usageError{e.t("completion.usage")}
		}
		return exitOK, nil
	}
}

// cmdSpec is what the completion scripts need to know about a command.
type cmdSpec struct {
	name, summary, argKind string
	flags                  []flagSpec
}

type flagSpec struct {
	name, usage, kind string // kind: file, dir, path, value or "" (a switch)
}

func completionSpecs(e *env) []cmdSpec {
	var out []cmdSpec
	for _, c := range commands() {
		if c.hidden {
			continue
		}
		fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
		c.define(e, fs)
		spec := cmdSpec{name: c.name, summary: c.summary(e), argKind: c.argKind}
		fs.VisitAll(func(f *flag.Flag) {
			kind := flagKind(f.Name)
			if kind == "" && valueName(f) != "" {
				kind = "value"
			}
			spec.flags = append(spec.flags, flagSpec{name: f.Name, usage: f.Usage, kind: kind})
		})
		spec.flags = append(spec.flags, flagSpec{name: "help", usage: e.t("completion.help_flag")})
		sort.Slice(spec.flags, func(i, j int) bool { return spec.flags[i].name < spec.flags[j].name })
		out = append(out, spec)
	}
	return out
}

func commandNames(specs []cmdSpec) string {
	var names []string
	for _, s := range specs {
		names = append(names, s.name)
	}
	return strings.Join(names, " ")
}

// oneLine keeps the first sentence part of a usage text for a completion menu.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len([]rune(s)) > 70 {
		s = string([]rune(s)[:69]) + "…"
	}
	return s
}

func writeBash(w io.Writer, specs []cmdSpec) {
	fmt.Fprint(w, `# bash completion for obx. Install: obx completion bash > /etc/bash_completion.d/obx
# (or add 'source <(obx completion bash)' to ~/.bashrc).
_obx() {
  local cur prev cmd flags
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD-1]}"
  cmd="${COMP_WORDS[1]}"
  if [ "$COMP_CWORD" -eq 1 ]; then
`)
	fmt.Fprintf(w, "    COMPREPLY=( $(compgen -W %q -- \"$cur\") )\n    return\n  fi\n", commandNames(specs))
	fmt.Fprint(w, "  case \"$prev\" in\n")
	byKind := map[string][]string{}
	for _, s := range specs {
		for _, f := range s.flags {
			if f.kind != "" {
				byKind[f.kind] = append(byKind[f.kind], "--"+f.name)
			}
		}
	}
	for _, kind := range []string{"file", "path", "dir", "value"} {
		if len(byKind[kind]) == 0 {
			continue
		}
		names := strings.Join(uniq(byKind[kind]), "|")
		switch kind {
		case "file", "path":
			fmt.Fprintf(w, "    %s) COMPREPLY=( $(compgen -f -- \"$cur\") ); return ;;\n", names)
		case "dir":
			fmt.Fprintf(w, "    %s) COMPREPLY=( $(compgen -d -- \"$cur\") ); return ;;\n", names)
		default:
			fmt.Fprintf(w, "    %s) COMPREPLY=(); return ;;\n", names)
		}
	}
	fmt.Fprint(w, "  esac\n  case \"$cmd\" in\n")
	for _, s := range specs {
		var names []string
		for _, f := range s.flags {
			names = append(names, "--"+f.name)
		}
		args := ""
		switch s.argKind {
		case "dir":
			args = `COMPREPLY=( $(compgen -d -- "$cur") )`
		case "shell":
			args = fmt.Sprintf(`COMPREPLY=( $(compgen -W %q -- "$cur") )`, strings.Join(shells, " "))
		case "command":
			args = fmt.Sprintf(`COMPREPLY=( $(compgen -W %q -- "$cur") )`, commandNames(specs))
		case "":
			if s.name == "config" {
				args = `COMPREPLY=( $(compgen -W "path" -- "$cur") )`
			}
		}
		fmt.Fprintf(w, "    %s)\n      flags=%q\n", s.name, strings.Join(names, " "))
		fmt.Fprintf(w, "      if [[ \"$cur\" == -* ]]; then COMPREPLY=( $(compgen -W \"$flags\" -- \"$cur\") ); return; fi\n")
		if args != "" {
			fmt.Fprintf(w, "      %s\n", args)
		}
		fmt.Fprint(w, "      ;;\n")
	}
	fmt.Fprint(w, "  esac\n}\ncomplete -o default -F _obx obx\n")
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// zshQuote escapes text for a single-quoted zsh _arguments spec.
func zshQuote(s string) string {
	s = oneLine(s)
	r := strings.NewReplacer("'", "'\\''", "[", "(", "]", ")", ":", "\\:")
	return r.Replace(s)
}

func writeZsh(w io.Writer, specs []cmdSpec) {
	fmt.Fprint(w, `#compdef obx
# zsh completion for obx. Install: obx completion zsh > "${fpath[1]}/_obx", then restart the shell
# (or add 'source <(obx completion zsh)' after compinit in ~/.zshrc).
_obx() {
  local -a commands
  commands=(
`)
	for _, s := range specs {
		fmt.Fprintf(w, "    '%s:%s'\n", s.name, zshQuote(s.summary))
	}
	fmt.Fprint(w, "  )\n  if (( CURRENT == 2 )); then\n    _describe 'command' commands\n    return\n  fi\n  local cmd=${words[2]}\n  shift words\n  (( CURRENT-- ))\n  case $cmd in\n")
	for _, s := range specs {
		fmt.Fprintf(w, "    %s)\n      _arguments \\\n", s.name)
		for _, f := range s.flags {
			action := ""
			switch f.kind {
			case "file", "path":
				action = ":file:_files"
			case "dir":
				action = ":folder:_files -/"
			case "value":
				action = ":value:"
			}
			fmt.Fprintf(w, "        '--%s[%s]%s' \\\n", f.name, zshQuote(f.usage), action)
		}
		switch s.argKind {
		case "dir":
			fmt.Fprint(w, "        '*:folder:_files -/'\n")
		case "shell":
			fmt.Fprintf(w, "        '1:shell:(%s)'\n", strings.Join(shells, " "))
		case "command":
			fmt.Fprintf(w, "        '1:command:(%s)'\n", commandNames(specs))
		case "url":
			fmt.Fprint(w, "        '1:portal address:'\n")
		default:
			if s.name == "config" {
				fmt.Fprint(w, "        '1:what:(path)'\n")
			} else {
				fmt.Fprint(w, "        '*::'\n")
			}
		}
		fmt.Fprint(w, "      ;;\n")
	}
	fmt.Fprint(w, "  esac\n}\nif [ \"$funcstack[1]\" = \"_obx\" ]; then\n  _obx \"$@\"\nelse\n  compdef _obx obx\nfi\n")
}

func fishQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, "'", `\'`).Replace(oneLine(s)) + "'"
}

func writeFish(w io.Writer, specs []cmdSpec) {
	fmt.Fprint(w, "# fish completion for obx. Install: obx completion fish > ~/.config/fish/completions/obx.fish\n")
	fmt.Fprint(w, "complete -c obx -f\n")
	for _, s := range specs {
		fmt.Fprintf(w, "complete -c obx -n __fish_use_subcommand -a %s -d %s\n", s.name, fishQuote(s.summary))
	}
	for _, s := range specs {
		cond := "'__fish_seen_subcommand_from " + s.name + "'"
		for _, f := range s.flags {
			extra := ""
			switch f.kind {
			case "file", "path":
				extra = " -r -F"
			case "dir":
				extra = " -r -a '(__fish_complete_directories)'"
			case "value":
				extra = " -r"
			}
			fmt.Fprintf(w, "complete -c obx -n %s -l %s%s -d %s\n", cond, f.name, extra, fishQuote(f.usage))
		}
		switch s.argKind {
		case "dir":
			fmt.Fprintf(w, "complete -c obx -n %s -a '(__fish_complete_directories)'\n", cond)
		case "shell":
			fmt.Fprintf(w, "complete -c obx -n %s -a '%s'\n", cond, strings.Join(shells, " "))
		case "command":
			fmt.Fprintf(w, "complete -c obx -n %s -a '%s'\n", cond, commandNames(specs))
		}
		if s.name == "config" {
			fmt.Fprintf(w, "complete -c obx -n %s -a path\n", cond)
		}
	}
}

func psQuote(s string) string { return "'" + strings.ReplaceAll(oneLine(s), "'", "''") + "'" }

func writePowerShell(w io.Writer, specs []cmdSpec) {
	fmt.Fprint(w, `# PowerShell completion for obx. Install: obx completion powershell >> $PROFILE
Register-ArgumentCompleter -Native -CommandName obx -ScriptBlock {
  param($wordToComplete, $commandAst, $cursorPosition)
  $words = @($commandAst.CommandElements | ForEach-Object { $_.ToString() })
  $commands = @{
`)
	for _, s := range specs {
		fmt.Fprintf(w, "    %s = %s\n", psQuote(s.name), psQuote(s.summary))
	}
	fmt.Fprint(w, "  }\n  $flags = @{\n")
	for _, s := range specs {
		var names []string
		for _, f := range s.flags {
			names = append(names, psQuote("--"+f.name))
		}
		fmt.Fprintf(w, "    %s = @(%s)\n", psQuote(s.name), strings.Join(names, ", "))
	}
	fmt.Fprint(w, `  }
  if ($words.Count -lt 2 -or ($words.Count -eq 2 -and $wordToComplete -ne '')) {
    $commands.Keys | Sort-Object | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
      [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $commands[$_])
    }
    return
  }
  $cmd = $words[1]
  if ($wordToComplete -like '-*' -and $flags.ContainsKey($cmd)) {
    $flags[$cmd] | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
      [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterName', $_)
    }
    return
  }
  Get-ChildItem -Path "$wordToComplete*" -ErrorAction SilentlyContinue | ForEach-Object {
    [System.Management.Automation.CompletionResult]::new($_.Name, $_.Name, 'ProviderItem', $_.FullName)
  }
}
`)
}

var _ = register(map[string]string{
	"cmd.completion.about": "Prints a script that lets your shell complete obx commands, options, folders and files when you press Tab. Supported shells: bash, zsh, fish and PowerShell. See the examples for where to put the script.",
	"completion.usage":     "give one shell: bash, zsh, fish or powershell.",
	"completion.help_flag": "show help for this command",
})
