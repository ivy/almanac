// Package format holds the formatting routes hostilevet refuses for a hostile value.
package format

import (
	"fmt"
	"log"
	"log/slog"
	"strings"

	"github.com/ivy/almanac/hostile"
)

type event struct {
	Number uint64
	Title  hostile.Of[string]
}

type hidden struct{ title hostile.Of[string] }

func routes(title hostile.Of[string], labels []hostile.Of[string], logger *log.Logger) {
	_ = fmt.Sprintf("%v", title)                // want `fmt.Sprintf formats a hostile value`
	_ = fmt.Sprint("title: ", title)            // want `fmt.Sprint formats a hostile value`
	_ = fmt.Errorf("bad title %v", title)       // want `fmt.Errorf formats a hostile value`
	fmt.Println(event{Number: 1, Title: title}) // want `fmt.Println formats a hostile value`
	fmt.Println(&hidden{title})                 // want `fmt.Println formats a hostile value`
	fmt.Println(labels)                         // want `fmt.Println formats a hostile value`
	log.Printf("%s", title)                     // want `log.Printf formats a hostile value`
	logger.Println(title)                       // want `log.Println formats a hostile value`

	var builder strings.Builder
	fmt.Fprintf(&builder, "%s", title) // want `fmt.Fprintf formats a hostile value`
}

func accepted(title hostile.Of[string], count uint64, repo hostile.Slug) {
	_ = fmt.Sprintf("%d in %s", count, repo)
	slog.Info("read", "title", title)
	_ = hostile.Map(title, strings.ToUpper)
}
