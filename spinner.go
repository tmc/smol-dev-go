package main

import (
	"os"
	"time"

	"github.com/briandowns/spinner"
)

func spin(suffix, finish string) func() {
	suffix = " " + suffix
	spinner := spinner.New(
		spinner.CharSets[14],
		40*time.Millisecond,
		spinner.WithWriter(os.Stderr),
	)
	spinner.Color("bold", "green")
	spinner.Suffix = suffix
	spinner.FinalMSG = finish
	spinner.Start()
	return spinner.Stop
}
