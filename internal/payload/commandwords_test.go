package payload

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// commandWords is every word a shell script runs as a command, with the
// lines it is on: a reader for the static check of firstboot.sh against an
// installed system's commands (environment_test.go), not a shell parser.
// It reads command positions -- a line's start, after ; & | ( { $( ` and
// the keywords that begin a command list -- past assignments and
// redirections, outside quotes but inside "$(...)", and leaves out case
// patterns (a word ending in an unmatched ")"), for's and case's words,
// functions the script defines, and the shell's own builtins and keywords.
// A heredoc's body is read as a script when it is one (its first line a
// shell's shebang), and skipped otherwise.
//
// What it cannot read it refuses, as "line N: why", so that a use of it is
// a failure, not a command no check sees: a -c command string that is not
// a literal (one that is, `su - u -c "defaults ..."`, is read); the command
// sudo, xargs or find -exec runs; a $(...) or `...` in an unquoted
// heredoc's body, which the outer shell runs; a plist body's -c.
func commandWords(script string) (map[string][]int, []string) {
	lines := strings.Split(script, "\n")
	words := map[string][]int{}
	var unreadable []string
	refuse := func(n int, why string) { unreadable = append(unreadable, fmt.Sprintf("line %d: %s", n, why)) }
	defined := map[string]bool{}
	paths := map[string]string{} // NAME=/abs/path, or NAME=${X:-/abs/path}: what "$NAME" runs
	for _, l := range lines {
		if m := funcDefRe.FindStringSubmatch(l); m != nil {
			defined[m[1]] = true
		}
		if m := pathAssignmentRe.FindStringSubmatch(l); m != nil {
			paths[m[1]] = m[2]
		}
	}
	var stack []byte      // quoting still open at the end of the line before
	var heredoc string    // the terminator of the body being skipped, or ""
	var terminator string // the terminator of a script heredoc read as lines
	var heredocDash, heredocQuoted bool
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if terminator != "" && strings.TrimLeft(l, "\t") == terminator {
			terminator = ""
			continue
		}
		if heredoc != "" {
			t := l
			if heredocDash {
				t = strings.TrimLeft(t, "\t")
			}
			if t == heredoc {
				heredoc = ""
				continue
			}
			if !heredocQuoted && (strings.Contains(l, "$(") || strings.Contains(l, "`")) {
				refuse(i+1, "a command substitution in an unquoted heredoc, which the outer shell runs: quote the delimiter, or run it before")
			}
			if strings.Contains(l, "<string>-c</string>") {
				refuse(i+1, "a plist runs a -c command string")
			}
			// Another language's body (python, a plist) runs what it names
			// by path: each quoted path in a directory on PATH is a command.
			for _, m := range bodyPathRe.FindAllStringSubmatch(l, -1) {
				words[m[1]] = append(words[m[1]], i+1)
			}
			continue
		}
		// A line continued after |, && or || starts a command; one
		// continued after an argument does not.
		continued := false
		if i > 0 && strings.HasSuffix(lines[i-1], "\\") {
			prev := strings.TrimRight(strings.TrimSuffix(lines[i-1], "\\"), " \t")
			continued = !strings.HasSuffix(prev, "|") && !strings.HasSuffix(prev, "&&")
		}
		var cmds []string
		cmds, stack = lineCommands(l, !continued && len(stack) == 0, stack)
		cmds = append(cmds, unreadableIn(l, cmds, func(why string) { refuse(i+1, why) })...)
		for _, w := range cmds {
			if strings.HasPrefix(w, "$") {
				if p, ok := paths[w[1:]]; ok {
					w = p // run through a variable the script set to a path
				}
			}
			if shellBuiltins[w] || defined[w] {
				continue
			}
			words[w] = append(words[w], i+1)
		}
		if m := heredocRe.FindStringSubmatch(l); m != nil && i+1 < len(lines) {
			first := lines[i+1]
			if m[1] == "-" {
				first = strings.TrimLeft(first, "\t")
			}
			if shellShebangRe.MatchString(first) {
				// a script: its lines are read as lines of this one, up to
				// its terminator
				terminator = strings.Trim(m[2], `'"\`)
				continue
			}
			heredoc, heredocDash = strings.Trim(m[2], `'"\`), m[1] == "-"
			heredocQuoted = strings.ContainsAny(m[2], `'"\`)
		}
	}
	return words, unreadable
}

// unreadableIn reads what one line's commands run that the reader cannot
// see on its own: the command words of a literal -c string, returned; and
// each construct it cannot read, refused.
func unreadableIn(l string, cmds []string, refuse func(string)) []string {
	has := func(names ...string) bool {
		for _, c := range cmds {
			for _, n := range names {
				if c == n || strings.HasSuffix(c, "/"+n) {
					return true
				}
			}
		}
		return false
	}
	var more []string
	if has("su", "sh", "bash", "dash", "ksh", "zsh") && dashCRe.MatchString(l) {
		if m := literalDashCRe.FindStringSubmatch(l); m != nil && !strings.HasPrefix(strings.TrimSpace(m[1]+m[2]), "$") {
			inner, _ := lineCommands(m[1]+m[2], true, nil)
			more = append(more, inner...)
		} else { // not a literal, or one whose command is an expansion
			refuse("a -c command string that is not a literal, which this reader cannot read")
		}
	}
	if has("sudo") {
		rest := redirectionRe.ReplaceAllString(l[strings.Index(l, "sudo")+len("sudo"):], "")
		rest, _, _ = strings.Cut(rest, ";")
		for _, w := range strings.Fields(rest) {
			if w == "|" || w == "&&" || w == "||" {
				break
			}
			if !sudoOnlyOptions[w] {
				refuse("sudo runs a command, which this reader does not read")
				break
			}
		}
	}
	if has("xargs") {
		refuse("xargs runs a command, which this reader does not read")
	}
	if has("find") && findExecRe.MatchString(l) {
		refuse("find runs a command (-exec, -ok), which this reader does not read")
	}
	return more
}

var (
	dashCRe = regexp.MustCompile(`\s-c(\s|$)`)
	// -c 'literal' or -c "literal": no command substitution in it
	literalDashCRe = regexp.MustCompile(`\s-c\s+(?:'([^']*)'|"((?:[^"\\$` + "`" + `]|\\.|\$[A-Za-z0-9_@*#?]|\$\{[^}]*\})*)")`)
	redirectionRe  = regexp.MustCompile(`[0-9]*[<>]+&?\S*`)
	findExecRe     = regexp.MustCompile(`\s-(exec|execdir|ok|okdir)(\s|$)`)
	// sudo with these alone runs nothing
	sudoOnlyOptions = map[string]bool{"-V": true, "-l": true, "-ll": true, "-v": true, "-k": true, "-K": true}
)

var (
	funcDefRe      = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s*\(\)\s*\{?`)
	heredocRe      = regexp.MustCompile(`<<(-?)\s*('[^']+'|"[^"]+"|\\?[A-Za-z0-9_]+)`)
	shellShebangRe = regexp.MustCompile(`^#!\s*(/bin/sh|/bin/bash|/usr/bin/env\s+(sh|bash))(\s|$)`)
	commandWordRe  = regexp.MustCompile(`^(/[A-Za-z0-9_./+-]+|[A-Za-z][A-Za-z0-9_.+-]*)$`)
	// NAME=/abs/path or NAME=${OVERRIDE:-/abs/path}, quoted or not
	pathAssignmentRe = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)="?(?:\$\{[A-Za-z_][A-Za-z0-9_]*:-)?(/[A-Za-z0-9_./+-]+)\}?"?\s*$`)
	// "$NAME", ${NAME} and the like, as a command
	varCommandRe = regexp.MustCompile(`^"?\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?"?(?:[\s;|&)]|$)`)
	// a quoted path in a directory on PATH, or a plist's <string>, in a body
	bodyPathRe   = regexp.MustCompile(`(?:['"]|<string>)(/(?:usr/)?s?bin/[A-Za-z0-9_.+-]+)(?:['"]|</string>)`)
	assignmentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\[[^]]*\])?\+?=`)
)

// commandWrappers are firstboot.sh's functions that run a command given
// after their own arguments, and how many of those there are.
var commandWrappers = map[string]int{"run_with_timeout": 1, "run_secret_with_timeout": 2, "nohup": 0, "env": 0}

// shellBuiltins are the words bash 3.2 and POSIX sh run themselves.
var shellBuiltins = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`if then else elif fi case esac for select while until do done in function time
		alias bg bind break builtin caller cd command compgen complete continue declare dirs disown echo enable
		eval exec exit export false fc fg getopts hash help history jobs kill let local logout popd printf
		pushd pwd read readonly return set shift shopt source suspend test times trap true type typeset ulimit
		umask unalias unset wait`) {
		m[w] = true
	}
	return m
}()

// lineCommands is the command words on one line of a script.
func lineCommands(l string, cmdPos bool, stack []byte) ([]string, []byte) {
	var out []string
	var word strings.Builder
	inWord := false
	skipUntil := ""      // after for/case: words are not commands until this keyword
	skipDepth := 0       // ... at this depth of quoting; inside a $( ) they are
	skipArgs := 0        // after a wrapper (run_with_timeout 120 cmd): its own arguments
	checkedFlag := false // run_with_timeout can put --checked before its limit
	var resume []bool    // per open $( ): whether the command position resumes after it
	// stack is the quoting open: '\'', '"', '(' for $( ... ), '`'
	top := func() byte {
		if len(stack) == 0 {
			return 0
		}
		return stack[len(stack)-1]
	}
	flush := func(next byte) {
		if !inWord {
			return
		}
		w := word.String()
		word.Reset()
		inWord = false
		if skipUntil != "" && len(stack) == skipDepth {
			if w == skipUntil {
				skipUntil = ""
				cmdPos = true
			}
			return
		}
		if !cmdPos {
			return
		}
		if skipArgs > 0 {
			if checkedFlag && w == "--checked" {
				checkedFlag = false
				return
			}
			checkedFlag = false
			skipArgs--
			return
		}
		if n, ok := commandWrappers[w]; ok {
			skipArgs = n
			checkedFlag = w == "run_with_timeout"
			out = append(out, w) // a command itself (a function's is dropped later)
			return               // and the command it runs comes next
		}
		switch {
		case assignmentRe.MatchString(w):
			return // still at the command position
		case w == "for" || w == "select":
			skipUntil, skipDepth = "do", len(stack)
			cmdPos = false
			return
		case w == "case":
			skipUntil, skipDepth = "in", len(stack)
			cmdPos = false
			return
		case w == "then" || w == "do" || w == "else" || w == "elif" || w == "if" || w == "while" || w == "until" || w == "!" || w == "time" || w == "{" || w == "}" || w == "fi" || w == "done" || w == "esac" || w == "command" || w == "exec" || w == "builtin":
			cmdPos = w != "fi" && w != "done" && w != "esac" && w != "}"
			return
		case next == ')' && top() == 0:
			return // a case pattern; its ")" starts the commands
		}
		cmdPos = false
		if commandWordRe.MatchString(w) {
			out = append(out, w)
		}
	}
	for i := 0; i < len(l); i++ {
		c := l[i]
		switch top() {
		case '\'':
			if c == '\'' {
				stack = stack[:len(stack)-1]
			}
			continue
		case '"':
			switch {
			case c == '\\':
				i++
			case c == '"':
				stack = stack[:len(stack)-1]
			case c == '$' && i+1 < len(l) && l[i+1] == '(' && !(i+2 < len(l) && l[i+2] == '('):
				// x="$(cmd)": as unquoted, the word before it is done
				resume = append(resume, cmdPos && inWord && assignmentRe.MatchString(word.String()))
				flush(c)
				stack = append(stack, '(')
				i++
				cmdPos = true
			case c == '`':
				stack = append(stack, '`')
				cmdPos = true
			}
			continue
		}
		// A command run through a variable: "$NAME", ${NAME}.
		if cmdPos && !inWord && skipArgs == 0 && (skipUntil == "" || len(stack) != skipDepth) {
			if m := varCommandRe.FindStringSubmatch(l[i:]); m != nil {
				out = append(out, "$"+m[1])
				cmdPos = false
				i += len(strings.TrimRight(m[0], " \t;|&)")) - 1
				continue
			}
		}
		switch {
		case c == '#' && !inWord:
			flush(0)
			return out, stack
		case c == '\\':
			i++
			inWord = true
		case c == '\'' || c == '"':
			stack = append(stack, c)
			inWord = true
		case c == '`':
			flush(c)
			if top() == '`' {
				stack = stack[:len(stack)-1]
				cmdPos = false
			} else {
				stack = append(stack, '`')
				cmdPos = true
			}
		case c == '$' && i+1 < len(l) && l[i+1] == '(':
			if i+2 < len(l) && l[i+2] == '(' {
				// $(( arithmetic )): to its end
				end := strings.Index(l[i:], "))")
				if end < 0 {
					return out, stack
				}
				i += end + 1
				inWord = true
				continue
			}
			// inside an assignment at the command position (x=$(cmd) next),
			// the command position resumes after it
			resume = append(resume, cmdPos && inWord && assignmentRe.MatchString(word.String()))
			flush(c)
			stack = append(stack, '(')
			i++
			cmdPos = true
		case c == '$' && i+1 < len(l) && l[i+1] == '{':
			// ${...}, which may hold spaces (${x:+a $y}): all one word
			depth, j := 0, i+1
			for ; j < len(l); j++ {
				if l[j] == '{' {
					depth++
				} else if l[j] == '}' {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			word.WriteString(l[i:min(j+1, len(l))])
			inWord = true
			i = j
		case c == '$' || c == '{' && inWord || c == '}' && inWord:
			inWord = true
			word.WriteByte(c)
		case c == ';' || c == '&' || c == '|' || c == '(' || c == '\n':
			flush(c)
			if c == ';' && i+1 < len(l) && l[i+1] == ';' {
				i++ // ;; ends a case branch: next is a pattern
				cmdPos = false
				continue
			}
			cmdPos = true
		case c == ')':
			flush(c)
			if top() == '(' {
				stack = stack[:len(stack)-1]
				// the end of a $( ): back to where it began
				cmdPos = len(resume) > 0 && resume[len(resume)-1]
				if len(resume) > 0 {
					resume = resume[:len(resume)-1]
				}
			} else {
				cmdPos = true // a case pattern's or a subshell's end
			}
		case c == '<' || c == '>':
			flush(c)
			// a redirection: skip its target word
			j := i + 1
			for j < len(l) && (l[j] == '>' || l[j] == '<' || l[j] == '&' || l[j] == ' ' || l[j] == '\t') {
				j++
			}
			for j < len(l) && l[j] != ' ' && l[j] != '\t' && l[j] != ';' && l[j] != '|' && l[j] != ')' {
				j++
			}
			i = j - 1
		case c == ' ' || c == '\t':
			flush(c)
		default:
			inWord = true
			word.WriteByte(c)
		}
	}
	flush(0)
	return out, stack
}

// sortedWords is words' keys, sorted.
func sortedWords(words map[string][]int) []string {
	var out []string
	for w := range words {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}
