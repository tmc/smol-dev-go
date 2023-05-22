package main

import (
	"time"

	"github.com/briandowns/spinner"
)

func spin(suffix string) func() {
	suffix = " " + suffix
	spinner := spinner.New(spinner.CharSets[14], 50*time.Millisecond, spinner.WithSuffix(suffix))
	spinner.Start()
	return spinner.Stop
}
