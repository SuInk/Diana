package emojinames

import (
	"bufio"
	"embed"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"sync"
)

//go:embed emoji-17.0-cldr-48/emoji-test.txt emoji-17.0-cldr-48/annotations*.xml
var data embed.FS

type Name struct {
	Emoji   string
	English string
	Chinese string
}

type node struct {
	children map[rune]*node
	name     *Name
}

var dictionary = sync.OnceValues(load)

func load() (*node, error) {
	chinese := map[string]string{}
	for _, path := range []string{"annotations-zh.xml", "annotations-derived-zh.xml"} {
		body, err := data.ReadFile("emoji-17.0-cldr-48/" + path)
		if err != nil {
			return nil, err
		}
		decoder := xml.NewDecoder(strings.NewReader(string(body)))
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			start, ok := token.(xml.StartElement)
			if !ok || start.Name.Local != "annotation" {
				continue
			}
			var entry struct {
				Emoji string `xml:"cp,attr"`
				Type  string `xml:"type,attr"`
				Text  string `xml:",chardata"`
			}
			if err := decoder.DecodeElement(&entry, &start); err != nil {
				return nil, err
			}
			if entry.Type == "tts" && entry.Text != "↑↑↑" {
				chinese[strings.ReplaceAll(entry.Emoji, "\ufe0f", "")] = strings.TrimSpace(entry.Text)
			}
		}
	}
	body, err := data.ReadFile("emoji-17.0-cldr-48/emoji-test.txt")
	if err != nil {
		return nil, err
	}
	root := &node{}
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		_, description, ok := strings.Cut(line, "#")
		fields := strings.Fields(description)
		if !ok || len(fields) < 3 {
			return nil, fmt.Errorf("invalid embedded emoji entry: %s", line)
		}
		name := &Name{Emoji: fields[0], English: strings.Join(fields[2:], " ")}
		name.Chinese = chinese[strings.ReplaceAll(name.Emoji, "\ufe0f", "")]
		current := root
		for _, r := range name.Emoji {
			if current.children == nil {
				current.children = map[rune]*node{}
			}
			if current.children[r] == nil {
				current.children[r] = &node{}
			}
			current = current.children[r]
		}
		current.name = name
	}
	return root, scanner.Err()
}

// Find returns distinct, longest matching emoji sequences in text order.
func Find(text string, limit int) ([]Name, error) {
	if limit <= 0 {
		return nil, nil
	}
	root, err := dictionary()
	if err != nil {
		return nil, err
	}
	runes := []rune(text)
	seen := map[string]bool{}
	var names []Name
	for i := 0; i < len(runes) && len(names) < limit; {
		current := root
		var match *Name
		end := i + 1
		for j := i; j < len(runes); j++ {
			current = current.children[runes[j]]
			if current == nil {
				break
			}
			if current.name != nil {
				match, end = current.name, j+1
			}
		}
		if match != nil && !seen[match.Emoji] {
			seen[match.Emoji] = true
			names = append(names, *match)
		}
		i = end
	}
	return names, nil
}
