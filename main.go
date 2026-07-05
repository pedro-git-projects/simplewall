package main

import (
	"log"
	"os"

	"gioui.org/app"
	"gioui.org/unit"
)

func main() {
	// --restore reapplies the latest wallpaper configuration previously applied
	// by the program, without opening the GUI.
	for _, arg := range os.Args[1:] {
		if arg == "--restore" {
			if err := restoreLast(); err != nil {
				log.Fatal(err)
			}
			return
		}
	}

	go func() {
		w := new(app.Window)
		w.Option(app.Title("Simple Wallpaper"), app.Size(unit.Dp(1100), unit.Dp(700)))
		a := newApp(w)
		if err := a.run(); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}
