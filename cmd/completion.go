package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// appended after Cobra's generated bash completion; captures the Cobra-registered handler, then overrides the `complete` registration with a wrapper that:
//   - delegates to Cobra for coral-native subcommands
//   - substitutes COMP_WORDS[0] with the active runtime and calls the runtime's own completion function for everything else
//
// the native command list (%s) is baked in at generation time
const bashPassthroughWrapper = `
# Capture cobra's completion handler before overriding it.
__coral_cobra_fn=$(complete -p coral 2>/dev/null | awk '{for(i=1;i<=NF;i++)if($i=="-F"){print $(i+1);exit}}')

__coral_passthrough() {
    local subcommand="${COMP_WORDS[1]:-}"
    local is_native=0
    if [[ ${COMP_CWORD} -le 1 ]]; then
        is_native=1
    else
        local native_cmds=(%s)
        for c in "${native_cmds[@]}"; do
            [[ "$subcommand" == "$c" ]] && is_native=1 && break
        done
    fi

    if [[ $is_native -eq 1 ]]; then
        [[ -n "$__coral_cobra_fn" ]] && "$__coral_cobra_fn" "$@"
        return
    fi

    # pass-through: swap in the runtime binary and call its completion function
    local rt="${CORAL_CONTAINER_RUNTIME:-docker}"
    local rt_fn
    rt_fn=$(complete -p "$rt" 2>/dev/null | awk '{for(i=1;i<=NF;i++)if($i=="-F"){print $(i+1);exit}}')
    # if the runtime's completion isn't loaded yet, trigger lazy loading
    if [[ -z "$rt_fn" ]]; then
        if declare -f _completion_loader >/dev/null 2>&1; then
            _completion_loader "$rt"
        else
            local _f
            for _d in /usr/share/bash-completion/completions /etc/bash_completion.d; do
                [[ -f "$_d/$rt" ]] && _f="$_d/$rt" && break
            done
            [[ -n "$_f" ]] && source "$_f" 2>/dev/null
        fi
        rt_fn=$(complete -p "$rt" 2>/dev/null | awk '{for(i=1;i<=NF;i++)if($i=="-F"){print $(i+1);exit}}')
    fi

    if [[ -n "$rt_fn" ]] && declare -f "$rt_fn" >/dev/null 2>&1; then
        local _w="${COMP_WORDS[0]}"
        local _line="$COMP_LINE"
        local _point="$COMP_POINT"
        COMP_WORDS[0]="$rt"
        COMP_LINE="${rt}${COMP_LINE#"${_w}"}"
        COMP_POINT=$(( COMP_POINT - ${#_w} + ${#rt} ))
        "$rt_fn" "$@"
        COMP_WORDS[0]="$_w"
        COMP_LINE="$_line"
        COMP_POINT="$_point"
    fi
}

complete -F __coral_passthrough coral
`

var completionCmd = &cobra.Command{
	Use:   "completion [bash|zsh|fish|powershell]",
	Short: "Generate completion script",
	Long: `Generate shell completion script for your shell. For example:

Bash:
	source <(coral completion bash)

Zsh:
	coral completion zsh > "${fpath[1]}/_coral"

Fish:
	coral completion fish | source
`,
	Args: cobra.MatchAll(
		cobra.ExactArgs(1),
		cobra.OnlyValidArgs,
	),
	ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
	Run: func(cmd *cobra.Command, args []string) {
		switch args[0] {
		case "bash":
			// write Cobra's static completion first (registers __start_coral) then append the passthrough wrapper which overrides it
			_ = cmd.Root().GenBashCompletion(os.Stdout)
			var native []string
			for _, c := range cmd.Root().Commands() {
				if !c.Hidden {
					native = append(native, c.Name())
				}
			}
			sort.Strings(native)
			fmt.Fprintf(os.Stdout, bashPassthroughWrapper, strings.Join(native, " "))
		case "zsh":
			cmd.Root().GenZshCompletion(os.Stdout)
		case "fish":
			cmd.Root().GenFishCompletion(os.Stdout, true)
		case "powershell":
			cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
		}
	},
}
