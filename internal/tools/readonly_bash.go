package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jrmarcco/goaw/internal/logging"
	"github.com/jrmarcco/goaw/internal/schema"
)

// readOnlyBinaries 只读命令白名单: 每个管道段的首 token 必须命中其一。
// 不在名单内的命令 ( 含未来新增的工具 ) 一律拒绝，默认从严。
var readOnlyBinaries = map[string]struct{}{
	"ls":    {},
	"cat":   {},
	"head":  {},
	"tail":  {},
	"grep":  {},
	"rg":    {},
	"find":  {},
	"wc":    {},
	"file":  {},
	"stat":  {},
	"tree":  {},
	"du":    {},
	"diff":  {},
	"which": {},
}

// readOnlyGitSubcommands git 只读子命令白名单。
// 代码考古 ( 谁改的、何时改的、改了什么 ) 对信息收集价值很高。
var readOnlyGitSubcommands = map[string]struct{}{
	"status":    {},
	"log":       {},
	"show":      {},
	"diff":      {},
	"blame":     {},
	"branch":    {},
	"tag":       {},
	"rev-parse": {},
	"ls-files":  {},
	"shortlog":  {},
	"describe":  {},
}

// findForbiddenFlags find 的副作用选项:
// -exec/-ok 可执行任意命令，-delete/-fprintf/-fprint 会写文件系统。
var findForbiddenFlags = map[string]struct{}{
	"-exec":    {},
	"-ok":      {},
	"-delete":  {},
	"-fprintf": {},
	"-fprint":  {},
}

// NewReadOnlyBashMiddleware 构建 bash 命令的只读约束中间件。
// 非 bash 工具直接放行 ( 注册表内的工具面由各角色装配层决定 )；
// bash 命令经白名单校验，任何解析不确定一律拒绝 ( fail-closed )。
//
// System Prompt 中的角色约束只是行为引导，本中间件才是安全边界:
// SubAgent 读到的仓库内容、日志属于不可信输入，
// 必须在能力层封死其诱导出的破坏性命令。
func NewReadOnlyBashMiddleware() Middleware {
	return func(_ context.Context, tc schema.ToolCall) (bool, string) {
		if tc.Name != bashToolName {
			return true, ""
		}

		var input bashArgs
		if err := json.Unmarshal(tc.Args, &input); err != nil {
			return false, "bash 参数解析失败，只读校验无法进行"
		}

		if reason := checkReadOnlyCommand(input.Command); reason != "" {
			logging.For("bash").Debug("只读策略拦截命令", "command", input.Command, "reason", reason)
			return false, fmt.Sprintf(
				"只读策略拒绝该命令: %s。仅允许只读命令 ( %s ) 与 git 只读子命令，"+
					"禁止重定向、命令替换、后台执行与工作区外路径",
				reason, strings.Join(readOnlyBinaryNames(), " "),
			)
		}
		return true, ""
	}
}

// checkReadOnlyCommand 校验 bash 命令是否只读，返回空字符串表示通过。
func checkReadOnlyCommand(cmd string) string {
	if strings.TrimSpace(cmd) == "" {
		return "空命令"
	}

	scanner := &commandScanner{}
	if reason := scanner.scan(cmd); reason != "" {
		return reason
	}

	for _, segment := range scanner.segments {
		if reason := validateReadOnlySegment(segment); reason != "" {
			return reason
		}
	}
	return ""
}

// readOnlyBinaryNames 白名单命令名 ( 排序保证拒绝文案稳定 )。
func readOnlyBinaryNames() []string {
	names := make([]string, 0, len(readOnlyBinaries))
	for name := range readOnlyBinaries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// readonlyToken 引号感知分词后的 token。
// quoted 表示 token 的任一部分来自引号包裹: 引号内是纯数据，
// 不参与路径卫生检查 ( 如 grep ">" 的模式 )。
type readonlyToken struct {
	text   string
	quoted bool
}

// commandScanner bash 命令的引号感知分词器。
// 产出按分隔符 ( | || && ; 换行 ) 切分的管道段，每段是 token 序列；
// 途中遇到的任何未放行语法 ( 重定向、命令替换、子 shell、后台执行、转义 )
// 立即返回拒绝原因，fail-closed。
type commandScanner struct {
	segments [][]readonlyToken
	tokens   []readonlyToken

	curr    strings.Builder
	hasCurr bool
	quoted  bool

	// pendingSeparator 分隔符后未出现新 token 时置位，
	// 命令以分隔符结尾 ( 如 "cat a |" ) 属于残缺语法，拒绝。
	pendingSeparator bool
}

// scan 逐字符扫描命令，返回拒绝原因 ( 空字符串表示扫描通过 )。
func (s *commandScanner) scan(cmd string) string {
	for i := 0; i < len(cmd); {
		next, reason := s.scanAt(cmd, i)
		if reason != "" {
			return reason
		}
		i = next
	}

	if s.pendingSeparator && !s.hasCurr {
		return "命令以分隔符结尾"
	}
	s.flushSegment()
	return ""
}

// scanAt 处理位置 i 的单个字符，返回处理后的下一个位置。
func (s *commandScanner) scanAt(cmd string, i int) (next int, rejectReason string) {
	ch := cmd[i]

	switch {
	case ch == '\'' || ch == '"':
		return s.scanQuoted(cmd, i)

	case ch == ' ', ch == '\t', ch == '\r':
		s.flushToken()
		return i + 1, ""

	case ch == '\n', ch == ';':
		s.flushSegment()
		return i + 1, ""

	case ch == '|':
		// | 与 || 都是管道段分隔符。
		s.flushSegment()
		return i + separatorLen(cmd, i, '|'), ""

	case ch == '&':
		return s.scanAmpersand(cmd, i)

	case isRejectedChar(ch):
		return 0, rejectCharReason(ch)

	case ch == '$' && i+1 < len(cmd) && cmd[i+1] == '(':
		return 0, "命令包含命令替换 ( $() )"

	default:
		s.appendLiteral(string(ch), false)
		return i + 1, ""
	}
}

// scanQuoted 处理引号片段，返回闭合引号后的下一个位置。
func (s *commandScanner) scanQuoted(cmd string, i int) (next int, rejectReason string) {
	if cmd[i] == '\'' {
		end, reason := scanSingleQuoted(cmd, i)
		if reason != "" {
			return 0, reason
		}
		s.appendLiteral(cmd[i+1:end], true)
		return end + 1, ""
	}

	end, reason := scanDoubleQuoted(cmd, i)
	if reason != "" {
		return 0, reason
	}
	s.appendLiteral(cmd[i+1:end], true)
	return end + 1, ""
}

// scanAmpersand 处理 &: && 是管道段分隔符，单个 & 是后台执行，拒绝。
func (s *commandScanner) scanAmpersand(cmd string, i int) (next int, rejectReason string) {
	if i+1 >= len(cmd) || cmd[i+1] != '&' {
		return 0, "命令包含后台执行符 (&)"
	}
	s.flushSegment()
	return i + separatorLen(cmd, i, '&'), ""
}

// isRejectedChar 判定引号外的危险单字符 ( 重定向、子 shell、命令替换、转义 )。
func isRejectedChar(ch byte) bool {
	switch ch {
	case '>', '<', '(', ')', '`', '\\':
		return true
	}
	return false
}

// rejectCharReason 给出危险字符的拒绝原因。
func rejectCharReason(ch byte) string {
	switch ch {
	case '>', '<':
		return "命令包含重定向 ( > < >> << )"
	case '(', ')':
		return "命令包含子 shell 括号"
	case '`':
		return "命令包含命令替换 ( 反引号 )"
	}
	return "命令包含反斜杠转义"
}

// appendLiteral 追加一个字面片段到当前 token。
func (s *commandScanner) appendLiteral(text string, quoted bool) {
	s.curr.WriteString(text)
	s.hasCurr = true
	if quoted {
		s.quoted = true
	}
	s.pendingSeparator = false
}

// flushToken 结束当前 token。
func (s *commandScanner) flushToken() {
	if !s.hasCurr {
		return
	}
	s.tokens = append(s.tokens, readonlyToken{text: s.curr.String(), quoted: s.quoted})
	s.curr.Reset()
	s.hasCurr = false
	s.quoted = false
}

// flushSegment 结束当前管道段。
func (s *commandScanner) flushSegment() {
	s.flushToken()
	if len(s.tokens) > 0 {
		s.segments = append(s.segments, s.tokens)
		s.tokens = nil
	}
	s.pendingSeparator = true
}

// separatorLen 返回分隔符宽度: 双字符形态 ( || / && ) 为 2，单字符为 1。
func separatorLen(cmd string, i int, ch byte) int {
	if i+1 < len(cmd) && cmd[i+1] == ch {
		return lenDoubleSeparator
	}
	return lenSingleSeparator
}

const (
	lenSingleSeparator = 1
	lenDoubleSeparator = 2

	// minGitTokens git 命令的最小 token 数 ( git + 子命令 )。
	minGitTokens = 2
)

// scanSingleQuoted 定位单引号闭合位置。
// 单引号内是全字面量，无需检查内容。
func scanSingleQuoted(cmd string, start int) (endIdx int, rejectReason string) {
	rel := strings.IndexByte(cmd[start+1:], '\'')
	if rel < 0 {
		return 0, "命令包含未闭合的单引号"
	}
	return start + 1 + rel, ""
}

// scanDoubleQuoted 定位双引号闭合位置。
// 双引号内的 $() 与反引号仍会被 shell 执行，必须拒绝；
// 反斜杠转义一并拒绝 ( fail-closed，杜绝转义绕过 )。
func scanDoubleQuoted(cmd string, start int) (endIdx int, rejectReason string) {
	for i := start + 1; i < len(cmd); i++ {
		switch cmd[i] {
		case '"':
			return i, ""
		case '`':
			return 0, "双引号内包含命令替换 ( 反引号 )"
		case '\\':
			return 0, "双引号内包含反斜杠转义"
		case '$':
			if i+1 < len(cmd) && cmd[i+1] == '(' {
				return 0, "双引号内包含命令替换 ( $() )"
			}
		}
	}
	return 0, "命令包含未闭合的双引号"
}

// validateReadOnlySegment 校验单个管道段:
// 首 token 白名单 + 命令级黑名单 + 路径卫生。
func validateReadOnlySegment(tokens []readonlyToken) string {
	head := tokens[0]
	if head.quoted {
		return "命令名不应被引号包裹"
	}
	if strings.Contains(head.text, "=") {
		return "不允许环境变量前缀 ( 如 FOO=bar )"
	}
	if reason := checkPathHygiene(tokens); reason != "" {
		return reason
	}
	return checkBinaryPolicy(tokens)
}

// checkPathHygiene 路径卫生 ( best-effort ):
// 引号外不允许绝对路径、~ 前缀与 .. 逃逸；引号内的字符串 ( 如 grep 模式 ) 不检查。
// 首 token 不参与 ( 由命令白名单管辖，如 /bin/ls 天然不在白名单内 )。
func checkPathHygiene(tokens []readonlyToken) string {
	for _, tok := range tokens[1:] {
		if tok.quoted {
			continue
		}
		switch {
		case strings.HasPrefix(tok.text, "/"):
			return fmt.Sprintf("路径 [%s] 是绝对路径，只允许工作区内相对路径", tok.text)
		case strings.HasPrefix(tok.text, "~"):
			return fmt.Sprintf("路径 [%s] 使用 ~ 前缀，只允许工作区内相对路径", tok.text)
		case strings.Contains(tok.text, ".."):
			return fmt.Sprintf("路径 [%s] 包含 ..，禁止逃逸工作区", tok.text)
		}
	}
	return ""
}

// checkBinaryPolicy 命令级策略: 白名单校验与命令特有的副作用选项拦截。
func checkBinaryPolicy(tokens []readonlyToken) string {
	head := tokens[0].text

	switch head {
	case "find":
		for _, tok := range tokens[1:] {
			if _, bad := findForbiddenFlags[tok.text]; bad {
				return "find 不允许 -exec/-ok/-delete 等具备副作用的选项"
			}
		}
		return ""
	case "git":
		return checkGitReadOnly(tokens)
	}

	if _, ok := readOnlyBinaries[head]; !ok {
		return fmt.Sprintf("命令 [%s] 不在只读白名单内", head)
	}
	return ""
}

// checkGitReadOnly git 子命令必须紧跟 git 且属于只读集合。
// 子命令前出现任何旗标 ( 如 -c ) 是配置注入向量，一律拒绝；
// 子命令后的旗标属于子命令自身语义 ( 如 git log -n 5 )，正常放行。
func checkGitReadOnly(tokens []readonlyToken) string {
	if len(tokens) < minGitTokens {
		return "git 缺少子命令"
	}

	sub := tokens[1]
	if sub.quoted || strings.HasPrefix(sub.text, "-") {
		return "git 子命令前不允许出现旗标 ( 配置注入向量 )"
	}
	if _, ok := readOnlyGitSubcommands[sub.text]; !ok {
		return fmt.Sprintf("git 子命令 [%s] 不在只读白名单内", sub.text)
	}
	return ""
}
