package main

import (
	"context"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"atomicgo.dev/keyboard"
	"atomicgo.dev/keyboard/keys"
	"github.com/bytedance/sonic"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/adk/middlewares/permission"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/mattn/go-runewidth"
	"github.com/pterm/pterm"
)

var (
	ctx         = context.Background()
	mu          sync.Mutex
	inputBuffer []rune
	cursorIndex int
	height      int

	allCmds = []string{
		commandExit,
		commandResume,
		commandModel,
	}
	suggestions []string

	fd       uintptr
	sigwinch chan os.Signal

	permissionActive atomic.Bool
	decisionChannel  = make(chan struct{})
	decisionSelected int

	promptTokens int
	cachedTokens int
)

const (
	defaultFixAreaRows              = 6
	defaultPermissionDialogAreaRows = 9

	banner = `
███████╗██╗███╗   ██╗ ██████╗  ██████╗██╗      █████╗ ██╗    ██╗
██╔════╝██║████╗  ██║██╔═══██╗██╔════╝██║     ██╔══██╗██║    ██║
█████╗  ██║██╔██╗ ██║██║   ██║██║     ██║     ███████║██║ █╗ ██║
██╔══╝  ██║██║╚██╗██║██║   ██║██║     ██║     ██╔══██║██║███╗██║
███████╗██║██║ ╚████║╚██████╔╝╚██████╗███████╗██║  ██║╚███╔███╔╝
╚══════╝╚═╝╚═╝  ╚═══╝ ╚═════╝  ╚═════╝╚══════╝╚═╝  ╚═╝ ╚══╝╚══╝ 
                                                                `
)

const (
	decisionOptionApprove = iota
	decisionOptionDeny
)

const (
	commandExit   = "exit"
	commandResume = "resume"
	commandModel  = "model"
)

// runTUILoop runs the TUI loop, interact with you in the terminal
func runTUILoop() {
	fd = os.Stdin.Fd()
	oldState, err := term.GetState(fd)
	if err != nil {
		log.Fatal(err)
	}
	term.MakeRaw(fd)

	defer func() {
		printRaw(ansi.SetTopBottomMargins(0, 0))
		printRaw(ansi.CursorPosition(1, pterm.GetTerminalHeight()))
		printRaw("\n")
		term.Restore(fd, oldState)
	}()

	height = pterm.GetTerminalHeight()

	colors := []func(a ...any) string{
		pterm.LightRed,
		pterm.LightCyan,
		pterm.LightBlue,
		pterm.LightGreen,
		pterm.LightYellow,
		pterm.LightMagenta,
	}
	printRaw(colors[rand.IntN(len(colors))](banner + strings.Repeat("\n", defaultFixAreaRows)))

	setScrollRegion()
	clearInputBar()

	go listenWinCh()

	err = keyboard.Listen(func(key keys.Key) (stop bool, err error) {
		if key.Code != keys.CtrlC && permissionActive.Load() {
			switch key.Code {

			case keys.Up:
				if decisionSelected > 0 {
					decisionSelected--
					mu.Lock()
					renderPermissionOptions()
					mu.Unlock()
				}

			case keys.Down:
				if decisionSelected < 1 {
					decisionSelected++
					mu.Lock()
					renderPermissionOptions()
					mu.Unlock()
				}

			case keys.Enter:
				decisionChannel <- struct{}{}
			}

			return false, nil
		}

		switch key.Code {

		case keys.CtrlC:
			turnLoop.Stop()
			turnLoop.Wait()
			return true, nil

		case keys.Backspace:
			mu.Lock()
			deleteCharBeforeCursor()
			mu.Unlock()

		case keys.Delete:
			mu.Lock()
			deleteCharAfterCursor()
			mu.Unlock()

		case keys.Left:
			mu.Lock()
			moveCursorLeft()
			mu.Unlock()

		case keys.Right:
			mu.Lock()
			moveCursorRight()
			mu.Unlock()

		case keys.Enter:
			text := string(inputBuffer)
			if text != "" {
				handled, exit := handleCommand(text)
				mu.Lock()
				clearInputBar()
				if exit {
					mu.Unlock()
					return true, nil
				}
				if handled {
					mu.Unlock()
					return false, nil
				}
				addChatLine("")
				addChatLine(pterm.Magenta("> ") + text)
				mu.Unlock()
				turnLoop.Push(chatItem{query: text})
			}

		case keys.RuneKey, keys.Space:
			mu.Lock()
			for _, r := range key.Runes {
				insertChatAtCursor(r)
			}
			mu.Unlock()
		}

		return false, nil
	})
	if err != nil {
		log.Fatal(err)
	}
}

// cursorCol returns the column number of the cursor in the terminal
func cursorCol() int {
	col := 2
	for i := 0; i < cursorIndex; i++ {
		col += runewidth.RuneWidth(inputBuffer[i])
	}
	return col + 1
}

// scrollBottom returns the line number of the bottom of the scrollable area
func scrollBottom() int {
	return pterm.GetTerminalHeight() - defaultFixAreaRows - len(suggestions)
}

// inputLineNum returns the line number of the input bar
func inputLineNum() int {
	return scrollBottom() + 4
}

// printRaw prints a string without converting newlines to \r\n
func printRaw(s string) {
	pterm.Print(strings.ReplaceAll(s, "\n", "\r\n"))
}

// setScrollRegion sets the scroll region to the bottom of the terminal
func setScrollRegion() {
	printRaw(ansi.SetTopBottomMargins(1, scrollBottom()))
}

// moveCursorToInputLine moves the cursor to the input line
func moveCursorToInputLine() {
	printRaw(ansi.CursorPosition(cursorCol(), inputLineNum()))
}

// moveCursorLeft moves the cursor to the left by one character
func moveCursorLeft() {
	if cursorIndex > 0 {
		cursorIndex--
	}
	moveCursorToInputLine()
}

// moveCursorRight moves the cursor to the right by one character
func moveCursorRight() {
	if cursorIndex < len(inputBuffer) {
		cursorIndex++
	}
	moveCursorToInputLine()
}

// renderTokenUsage renders the token usage
func renderTokenUsage() {
	printRaw(ansi.CursorPosition(1, inputLineNum()-2))
	printRaw(ansi.EraseEntireLine)
	printRaw(pterm.Gray(fmt.Sprintf(
		"  Context %s  │  Cached %s",
		pterm.White(formatNum(promptTokens)),
		pterm.LightGreen(formatNum(cachedTokens)),
	)))
}

func formatNum(n int) string {
	str := fmt.Sprintf("%d", n)
	if n < 1024 {
		return str
	}
	return fmt.Sprintf("%.1fK", float64(n)/1024)
}

// renderInputBar redraws the input bar
func renderInputBar() {
	printRaw(ansi.ShowCursor)
	printRaw(ansi.CursorPosition(1, inputLineNum()-3))
	printRaw(ansi.EraseScreenBelow)
	renderTokenUsage()
	printRaw(ansi.CursorPosition(1, inputLineNum()-1))
	printRaw(strings.Repeat("-", pterm.GetTerminalWidth()))
	printRaw(ansi.CursorPosition(1, inputLineNum()))
	printRaw(pterm.Bold.Sprint("❯ ") + string(inputBuffer))
	printRaw(ansi.CursorPosition(1, inputLineNum()+1))
	printRaw(strings.Repeat("-", pterm.GetTerminalWidth()))
	printRaw(ansi.CursorPosition(1, inputLineNum()+2))
	printRaw(pterm.Gray("[Press Ctrl+C to exit]"))
	moveCursorToInputLine()
}

// clearInputBar clears the input bar
func clearInputBar() {
	cursorIndex = 0
	inputBuffer = inputBuffer[:0]
	renderInputBar()
}

// insertChatAtCursor inserts a character at the cursor position
func insertChatAtCursor(r rune) {
	if cursorIndex >= len(inputBuffer) {
		inputBuffer = append(inputBuffer, r)
	} else {
		inputBuffer = append(inputBuffer, ' ')
		copy(inputBuffer[cursorIndex+1:], inputBuffer[cursorIndex:])
		inputBuffer[cursorIndex] = r
	}
	cursorIndex++
	renderInputBar()
	refreshSuggestions()
	moveCursorToInputLine()
}

// deleteCharFromInputBar deletes the character at the cursor position
func deleteCharBeforeCursor() {
	if cursorIndex == 0 {
		return
	}
	inputBuffer = append(inputBuffer[:cursorIndex-1], inputBuffer[cursorIndex:]...)
	cursorIndex--
	renderInputBar()
	refreshSuggestions()
	moveCursorToInputLine()
}

// deleteCharAfterCursor deletes the character after the cursor position
func deleteCharAfterCursor() {
	if cursorIndex >= len(inputBuffer) {
		return
	}
	inputBuffer = append(inputBuffer[:cursorIndex], inputBuffer[cursorIndex+1:]...)
	renderInputBar()
	refreshSuggestions()
	moveCursorToInputLine()
}

// renderSuggestions renders the suggestions
func renderSuggestions() {
	for i, cmd := range suggestions {
		printRaw(ansi.CursorPosition(1, inputLineNum()+2+i))
		printRaw(ansi.EraseEntireLine)
		pterm.Print(pterm.Gray(cmd))
	}
}

// refreshSuggestions refreshes the suggestions and renders the suggestion area
func refreshSuggestions() {
	input := string(inputBuffer)

	var newSuggestions []string
	if strings.HasPrefix(input, "/") {
		input = input[1:]
		for _, cmd := range allCmds {
			if strings.HasPrefix(cmd, input) {
				newSuggestions = append(newSuggestions, cmd)
			}
		}
	}

	d := len(newSuggestions) - len(suggestions)
	suggestions = newSuggestions

	scrollSreen(d)
	renderSuggestions()
}

// scrollSreen scrolls the screen by d lines up or down
func scrollSreen(d int) {
	if d == 0 {
		return
	}
	printRaw(ansi.SetTopBottomMargins(0, 0))
	if d > 0 {
		printRaw(ansi.ScrollUp(d))
	} else {
		printRaw(ansi.ScrollDown(-d))
	}
	setScrollRegion()
}

// addChatLine adds a line of chat to the screen
func addChatLine(s string) {
	printRaw(ansi.CursorPosition(1, scrollBottom()))
	printRaw("\n")
	printRaw(s)
	printRaw(ansi.SaveCurrentCursorPosition)
	moveCursorToInputLine()
}

// appendToChat appends a text to the chat line
func appendToChat(s string) {
	printRaw(ansi.RestoreCurrentCursorPosition)
	printRaw(s)
	printRaw(ansi.SaveCurrentCursorPosition)
	moveCursorToInputLine()
}

// renderToolCall renders tool calls
func renderToolCall(message adk.AgenticMessage) {
	if message == nil {
		return
	}
	for _, block := range message.ContentBlocks {
		call := block.FunctionToolCall
		if call == nil {
			continue
		}
		args := make(map[string]any)
		if err := sonic.UnmarshalString(call.Arguments, &args); err != nil {
			log.Println(err)
		}
		mu.Lock()
		addChatLine(" ")
		name := call.Name
		switch name {
		case filesystem.ToolNameLs:
			renderToolCallLs(args)
		case filesystem.ToolNameReadFile:
			renderToolCallReadFile(args)
		case filesystem.ToolNameWriteFile:
			renderToolCallWriteFile(args)
		case filesystem.ToolNameEditFile:
			renderToolCallEditFile(args)
		case filesystem.ToolNameGlob:
			renderToolCallGlob(args)
		case filesystem.ToolNameGrep:
			renderToolCallGrep(args)
		case filesystem.ToolNameExecute:
			renderToolCallExecute(args)
		case "skill":
			renderToolCallSkill(args)
		default:
			renderToolCallDefault(name, call.Arguments)
		}
		mu.Unlock()
	}
}

func renderToolCallSkill(args map[string]any) {
	skill := ""
	if v, ok := args["skill"].(string); ok {
		skill = v
	}
	appendToChat(pterm.LightMagenta("Use skill: ") + skill)
}

func renderToolCallLs(args map[string]any) {
	path := ""
	if v, ok := args["path"].(string); ok {
		path = v
	}
	appendToChat(pterm.LightGreen("List files: ") + path)
}

func renderToolCallReadFile(args map[string]any) {
	filePath, limit, offset := "", 0, 0
	if v, ok := args["file_path"].(string); ok {
		filePath = v
	}
	if v, ok := args["limit"].(int); ok {
		limit = v
	}
	if v, ok := args["offset"].(int); ok {
		offset = v
	}
	end := "end"
	if limit > 0 {
		end = fmt.Sprintf("%d", limit+offset-1)
	}
	appendToChat(pterm.LightGreen("Read file: ") + filePath + fmt.Sprintf(" (%d ~ %s)", offset, end))
}

func renderToolCallWriteFile(args map[string]any) {
	filePath := ""
	if v, ok := args["file_path"].(string); ok {
		filePath = v
	}
	appendToChat(pterm.LightGreen("Write file: ") + filePath)
}

func renderToolCallEditFile(args map[string]any) {
	filePath := ""
	if v, ok := args["file_path"].(string); ok {
		filePath = v
	}
	appendToChat(pterm.LightGreen("Edit file: ") + filePath)
}

func renderToolCallGlob(args map[string]any) {
	path, pattern := ".", ""
	if v, ok := args["path"].(string); ok {
		path = v
	}
	if v, ok := args["pattern"].(string); ok {
		pattern = v
	}
	appendToChat(pterm.LightGreen("Glob files: ") + pattern + " in " + path)
}

func renderToolCallGrep(args map[string]any) {
	path, pattern := ".", ""
	if v, ok := args["path"].(string); ok {
		path = v
	}
	if v, ok := args["pattern"].(string); ok {
		pattern = v
	}
	appendToChat(pterm.LightGreen("Grep files: ") + "\"" + pattern + "\" in " + path)
}

func renderToolCallExecute(args map[string]any) {
	command := ""
	if v, ok := args["command"].(string); ok {
		command = v
	}
	appendToChat(pterm.LightGreen("Execute command: ") + command)
}

func renderToolCallDefault(name string, arguments string) {
	if len(arguments) > 10 {
		arguments = arguments[:10] + "..."
	}
	appendToChat(pterm.LightGreen("Call tool: ") + name + " (" + arguments + ")")
}

func renderToolResult(message adk.AgenticMessage) {
	if message == nil {
		return
	}
	for _, block := range message.ContentBlocks {
		result := block.FunctionToolResult
		if result == nil {
			continue
		}
		mu.Lock()
		if result.Name != "" {
			appendToChat(pterm.LightYellow(" [tool result] ") + result.Name)
			addChatLine("")
		}
		for _, content := range result.Content {
			if content == nil {
				continue
			}
			if content.Text != nil {
				text := content.Text.Text
				if text != "" {
					maxLineNum := 40
					if lines := strings.Split(text, "\n"); len(lines) > maxLineNum {
						half, total := maxLineNum/2, len(lines)
						newLines := lines[:half]
						newLines = append(newLines, pterm.Magenta(pterm.Bold.Sprint(fmt.Sprintf("\n(truncate %d lines...)\n", total-maxLineNum))))
						newLines = append(newLines, lines[total-half:]...)
						text = strings.Join(newLines, "\n")
					}
					appendToChat(text)
				}
			}
		}
		mu.Unlock()
	}
}

func renderSummarization(action *summarization.TypedCustomizedAction[adk.AgenticMessage]) {
	mu.Lock()
	switch action.Type {
	case summarization.ActionTypeBeforeSummarize:
		addChatLine(pterm.Magenta("[summarizing...]"))
	case summarization.ActionTypeAfterSummarize:
		addChatLine(pterm.Magenta("[summarized]"))
		finalMsgs := action.After.Messages
		processed := finalMsgs[len(finalMsgs)-1]
		for _, block := range processed.ContentBlocks {
			if block.UserInputText != nil {
				addChatLine(block.UserInputText.Text)
			}
		}
		addChatLine("")
		addChatLine(pterm.Magenta("[chat continue below]"))
	}
	mu.Unlock()
}

// listenWinCh listens for window size changes
func listenWinCh() {
	sigwinch = make(chan os.Signal, 1)
	signal.Notify(sigwinch, syscall.SIGWINCH)

	for range sigwinch {
		newHeight := pterm.GetTerminalHeight()
		if newHeight != height {
			mu.Lock()
			d := height - newHeight
			height = newHeight
			scrollSreen(d)
			renderInputBar()
			mu.Unlock()
		}
	}
}

// scrollBottomWhenPermissionActive returns the scroll bottom when permission is active
func permissionScrollBottom() int {
	return pterm.GetTerminalHeight() - defaultPermissionDialogAreaRows
}

// promptUserForPermission gets user's permission decision, blocking
func promptUserForPermission(command string) permission.ResumeAction {
	permissionActive.Store(true)
	decisionSelected = 0
	mu.Lock()
	printRaw(ansi.HideCursor)
	scrollSreen(scrollBottom() - permissionScrollBottom())
	renderPermissionHeader(command)
	renderPermissionOptions()
	mu.Unlock()
	<-decisionChannel
	mu.Lock()
	printRaw(ansi.CursorPosition(1, permissionScrollBottom()+1))
	printRaw(ansi.EraseScreenBelow)
	scrollSreen(permissionScrollBottom() - scrollBottom())
	renderInputBar()
	mu.Unlock()
	permissionActive.Store(false)
	switch decisionSelected {
	case decisionOptionApprove:
		return permission.ResumeActionApprove
	case decisionOptionDeny:
		return permission.ResumeActionReject
	default:
		panic("unreachable")
	}
}

// renderPermissionHeader renders permission header
func renderPermissionHeader(command string) {
	startLine := permissionScrollBottom() + 1
	printRaw(ansi.CursorPosition(1, startLine))
	printRaw(ansi.EraseScreenBelow)
	printRaw(pterm.LightMagenta(strings.Repeat("-", pterm.GetTerminalWidth())))
	printRaw(ansi.CursorPosition(1, startLine+1))
	printRaw(pterm.Bold.Sprint(pterm.LightMagenta("Bash Command")))
	printRaw(ansi.CursorPosition(1, startLine+3))
	printRaw("  " + command)
	printRaw(ansi.CursorPosition(1, startLine+5))
	printRaw("是否执行此命令?")
}

// renderPermissionOptions renders permission options
func renderPermissionOptions() {
	options := []string{"同意", "拒绝"}
	for i, option := range options {
		printRaw(ansi.CursorPosition(3, pterm.GetTerminalHeight()-2+i))
		printRaw(ansi.EraseEntireLine)

		cursor := " "
		number := pterm.Gray(fmt.Sprintf("%d.", i+1))

		if decisionSelected == i {
			cursor = pterm.Bold.Sprint(pterm.LightMagenta(">"))
			option = pterm.Magenta(option)
		}

		printRaw(cursor + " " + number + " " + option)
	}
}

func handleCommand(command string) (handled bool, exit bool) {
	if len(command) == 0 || command[0] != '/' {
		return false, false
	}
	command = command[1:]
	parts := strings.Split(command, " ")
	command = parts[0]
	switch command {

	case commandExit:
		turnLoop.Stop()
		turnLoop.Wait()
		return true, true

	case commandResume:
		if len(parts) >= 2 {
			sessionID = parts[1]
			turnLoop.Stop()
			turnLoop.Wait()
			loadTurnLoopAndRun()
			return true, false
		}

	case commandModel:
		sessionID = parts[1]
		if len(parts) >= 2 {
			index, err := strconv.Atoi(parts[1])
			if err != nil {
				return false, false
			}
			loadModelAndAgent(index)
			return true, false
		}
	}

	return false, false
}
