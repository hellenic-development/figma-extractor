package figma

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantKey  string
		wantType string
		wantIDs  []string
		wantErr  error
	}{
		{"design link with a dash id", "https://www.figma.com/design/rrjFDZ1mXkjC147DGMDtFU/Mobile-Apps-%E2%80%93-Prototyping-Kit--Community-?node-id=193-3231&p=f&t=jQIqfqrH4tIfi4Ey-0", "rrjFDZ1mXkjC147DGMDtFU", "design", []string{"193:3231"}, nil},
		{"encoded colon", "https://www.figma.com/file/ABC123/Design?node-id=1%3A2", "ABC123", "file", []string{"1:2"}, nil},
		{"instance path", "https://www.figma.com/design/ABC123/Design?node-id=I5666-180910%3B1-10515", "ABC123", "design", []string{"I5666:180910;1:10515"}, nil},
		{"prototype link", "https://www.figma.com/proto/ABC123/Flow?node-id=3-4&starting-point-node-id=1-2", "ABC123", "proto", []string{"3:4"}, nil},
		{"prototype starting point only", "https://www.figma.com/proto/ABC123/Flow?starting-point-node-id=1-2", "ABC123", "proto", []string{"1:2"}, nil},
		{"figjam board", "https://www.figma.com/board/ABC123/Retro", "ABC123", "board", nil, nil},
		{"slides", "https://www.figma.com/slides/ABC123/Deck", "ABC123", "slides", nil, nil},
		{"branch uses the branch key", "https://www.figma.com/design/MAIN111/branch/BRANCH222/Name?node-id=1-2", "BRANCH222", "design", []string{"1:2"}, nil},
		{"embed host", "https://embed.figma.com/design/ABC123/Name?node-id=1-2&embed-host=share", "ABC123", "design", []string{"1:2"}, nil},
		{"embed wrapper", "https://www.figma.com/embed?embed_host=share&url=https%3A%2F%2Fwww.figma.com%2Fdesign%2FABC123%2FName%3Fnode-id%3D1-2", "ABC123", "design", []string{"1:2"}, nil},
		{"no scheme", "figma.com/design/ABC123/Name?node-id=1-2", "ABC123", "design", []string{"1:2"}, nil},
		{"surrounding spaces", "  https://www.figma.com/design/ABC123/Name  ", "ABC123", "design", nil, nil},
		{"key only", "https://www.figma.com/design/ABC123", "ABC123", "design", nil, nil},

		{"community link", "https://www.figma.com/community/file/1234567890/some-kit", "", "", nil, ErrCommunityURL},
		{"lookalike host", "https://www.figma.com.evil.example/design/ABC123/Name", "", "", nil, ErrNotFigmaURL},
		{"host that ends the same", "https://notfigma.com/design/ABC123/Name", "", "", nil, ErrNotFigmaURL},
		{"another scheme", "ftp://www.figma.com/design/ABC123/Name", "", "", nil, ErrNotFigmaURL},
		{"dashboard page", "https://www.figma.com/files/recents-and-sharing", "", "", nil, ErrNotFigmaURL},
		{"key with a symbol", "https://www.figma.com/design/ABC_123/Name", "", "", nil, ErrNotFigmaURL},
		{"not a link", "the hero frame", "", "", nil, ErrNotFigmaURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := ParseURL(tt.url)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if ref.FileKey != tt.wantKey || ref.FileType != tt.wantType {
				t.Errorf("key %q type %q, want %q %q", ref.FileKey, ref.FileType, tt.wantKey, tt.wantType)
			}
			if !reflect.DeepEqual(ref.NodeIDs, tt.wantIDs) {
				t.Errorf("node ids %v, want %v", ref.NodeIDs, tt.wantIDs)
			}
		})
	}
}

func TestNormalizeNodeID(t *testing.T) {
	tests := map[string]string{
		"12:34":      "12:34",
		" 12-34 ":    "12:34",
		"56%3A78":    "56:78",
		"I1:2;3-4":   "I1:2;3:4",
		"I1-2%3B3-4": "I1:2;3:4",
		"":           "",
	}
	for in, want := range tests {
		if got := NormalizeNodeID(in); got != want {
			t.Errorf("NormalizeNodeID(%q) = %q, want %q", in, got, want)
		}
	}
	got := NormalizeNodeIDs([]string{" 12-34 ", "", "12:34", "56%3A78"})
	if !reflect.DeepEqual(got, []string{"12:34", "56:78"}) {
		t.Errorf("NormalizeNodeIDs = %v", got)
	}
}
