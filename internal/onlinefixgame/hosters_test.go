package onlinefixgame

import (
	"context"
	"crypto/sha256"
	"testing"
)

func TestParseHosterLinks(t *testing.T) {
	html := `<div class="option" data-links='[{"direct_link":"https://pixeldrain.com/u/abc123","file_name":"Game.part01.rar","id":1,"is_dangerous":false}]'></div>`
	links, err := parseHosterLinks(html)
	if err != nil {
		t.Fatalf("parseHosterLinks: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("got %d links, want 1", len(links))
	}
	if links[0].FileName != "Game.part01.rar" {
		t.Errorf("file name = %q", links[0].FileName)
	}
	if links[0].DirectLink != "https://pixeldrain.com/u/abc123" {
		t.Errorf("direct link = %q", links[0].DirectLink)
	}
}

func TestParseHosterLinksHTMLEscaped(t *testing.T) {
	// data-links attributes arrive HTML-escaped (&quot; for ").
	html := `<div data-links='[{&quot;direct_link&quot;:&quot;https://fileditch.com/f/xyz&quot;,&quot;file_name&quot;:&quot;Game.rar&quot;}]'></div>`
	links, err := parseHosterLinks(html)
	if err != nil {
		t.Fatalf("parseHosterLinks: %v", err)
	}
	if len(links) != 1 || links[0].FileName != "Game.rar" {
		t.Fatalf("unexpected links: %+v", links)
	}
}

func TestIsFixRepair(t *testing.T) {
	if !isFixRepair("Game_Fix_Repair_Steam_Generic.rar") {
		t.Error("expected fix-repair match")
	}
	if isFixRepair("Game.v1.2-OFME.rar") {
		t.Error("unexpected fix-repair match")
	}
}

func TestComparePartNamesNumeric(t *testing.T) {
	if comparePartNames("Game.part2.rar", "Game.part10.rar") >= 0 {
		t.Error("part2 should sort before part10")
	}
	if comparePartNames("Game.part10.rar", "Game.part2.rar") <= 0 {
		t.Error("part10 should sort after part2")
	}
}

func TestExtractHiddenValue(t *testing.T) {
	html := `<input type="hidden" name="pow_challenge" value="abc123">
		<input type="hidden" name="pow_diff"      value="18">
		<input type="hidden" name="pow_nonce" id="pow_nonce" value="">`
	if got := extractHiddenValue(html, "pow_challenge"); got != "abc123" {
		t.Errorf("pow_challenge = %q", got)
	}
	if got := extractHiddenValue(html, "pow_diff"); got != "18" {
		t.Errorf("pow_diff = %q", got)
	}
	if got := extractHiddenValue(html, "missing"); got != "" {
		t.Errorf("missing = %q", got)
	}
}

func TestSolveFileditchPoW(t *testing.T) {
	const diff = 12
	nonce, err := solveFileditchPoW(context.Background(), "challenge-token", diff)
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	sum := sha256.Sum256([]byte("challenge-token:" + nonce))
	// 12 bits => first byte zero and the high nibble of the second byte zero.
	if sum[0] != 0 || sum[1]&0xF0 != 0 {
		t.Errorf("digest does not meet difficulty: %x", sum)
	}
}

func TestExtractFileditchLink(t *testing.T) {
	html := `<script>var u = ["https:\/\/1",".frea","kingfil","editch.me","\/alph","a16\/","file.rar?m","d5=SIG","&ex","pires=1784919924"].join("");</script>`
	got := extractFileditchLink(html)
	want := "https://1.freakingfileditch.me/alpha16/file.rar?md5=SIG&expires=1784919924"
	if got != want {
		t.Errorf("link = %q, want %q", got, want)
	}
	if extractFileditchLink("<html>no link</html>") != "" {
		t.Error("expected empty link")
	}
}

func TestGofileFileID(t *testing.T) {
	got := gofileFileID("https://file-eu-ldn-1.gofile.io/download/web/ce98ba03-b9ea/Game.part2.rar")
	if got != "ce98ba03-b9ea" {
		t.Errorf("file id = %q", got)
	}
	if gofileFileID("https://pixeldrain.com/api/file/abc") != "" {
		t.Error("expected empty file id")
	}
}

func TestSafeFileName(t *testing.T) {
	if got := safeFileName("../bad:name?.zip"); got != ".._bad_name_.zip" {
		t.Errorf("safeFileName = %q", got)
	}
}

func TestMergeFilesDedupAndOrder(t *testing.T) {
	files := []resolvedFile{
		{fileName: "Game.part2.rar", size: 100, mirror: mirror{provider: providerGofile, priority: priorityGofile}},
		{fileName: "Game.part1.rar", size: 200, mirror: mirror{provider: providerFileditch, priority: priorityFileditch}},
		{fileName: "Game.part1.rar", size: 200, sha256: "abc", mirror: mirror{provider: providerPixeldrain, priority: priorityPixeldrain}},
	}
	parts := mergeFiles(files)
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2", len(parts))
	}
	if parts[0].fileName != "Game.part1.rar" {
		t.Errorf("first part = %q, want Game.part1.rar", parts[0].fileName)
	}
	if len(parts[0].mirrors) != 2 {
		t.Fatalf("part1 mirrors = %d, want 2", len(parts[0].mirrors))
	}
	// Mirrors sorted by priority: fileditch(0) before pixeldrain(20).
	if parts[0].mirrors[0].provider != providerFileditch {
		t.Errorf("first mirror = %q, want fileditch", parts[0].mirrors[0].provider)
	}
	if parts[0].sha256 != "abc" {
		t.Errorf("sha256 not carried over: %q", parts[0].sha256)
	}
}

func TestScrapeTitle(t *testing.T) {
	cases := map[string]string{
		`<h1 class="title">Elden Ring по сети</h1>`:         "Elden Ring",
		`<title>Elden Ring по сети » online-fix.me</title>`: "Elden Ring",
		`<h1>Some Game</h1>`: "Some Game",
	}
	for in, want := range cases {
		if got := scrapeTitle(in); got != want {
			t.Errorf("scrapeTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
