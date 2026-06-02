// Command packwright is the entry point for the Packwright CLI.
//
// Packwright is a hybrid terminal/graphical tool for generating and managing
// AWS infrastructure templates. All command wiring lives in the cmd package ;
// main simply delegates to cmd.Execute.
package main

import "github.com/bannaarr01/packwright/cmd"

func main() {
	cmd.Execute()
}
