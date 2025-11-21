package logging

import (
	"github.com/fatih/color"
)

var (
	Red    = color.New(color.FgRed).SprintFunc()
	Green  = color.New(color.FgGreen).SprintFunc()
	Yellow = color.New(color.FgYellow).SprintFunc()
	Blue   = color.New(color.FgBlue).SprintFunc()
)

var (
	WhiteOnMagenta   = color.New(color.FgWhite, color.BgMagenta).SprintFunc()
	BoldMagenta      = color.New(color.FgMagenta, color.Bold).SprintFunc()
	BoldMagentaHi    = color.New(color.FgHiMagenta, color.Bold).SprintFunc()
	UnderlineMagenta = color.New(color.FgMagenta, color.Underline).SprintFunc()
)

func Info(msg string) string {
	return Blue("[INFO] ") + msg
}

func Success(msg string) string {
	return Green("[SUCCESS] ") + msg
}

func Warning(msg string) string {
	return Yellow("[WARNING] ") + msg
}

func Failure(msg string) string {
	return Red("[FAILURE] ") + msg
}
