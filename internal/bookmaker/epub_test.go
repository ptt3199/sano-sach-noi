package bookmaker

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEPUB(t *testing.T, entries []zipEntry) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "book.epub")
	writeZip(t, name, entries)
	return name
}

func TestParseEPUB_NavigationSpineAndImages(t *testing.T) {
	bookPath := writeEPUB(t, []zipEntry{
		{"META-INF/container.xml", []byte(`<container><rootfiles><rootfile full-path="OEBPS/content.opf"/></rootfiles></container>`)},
		{"OEBPS/content.opf", []byte(`<package><metadata><title>EPUB title</title><creator>Author Name</creator><meta property="cover-image" content="cover"/></metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="one" href="text/one.xhtml" media-type="application/xhtml+xml"/><item id="two" href="text/two.xhtml" media-type="application/xhtml+xml"/><item id="cover" href="images/cover.png" media-type="image/png" properties="cover-image"/><item id="inside" href="images/inside.png" media-type="image/png"/></manifest><spine><itemref idref="one"/><itemref idref="two"/></spine></package>`)},
		{"OEBPS/nav.xhtml", []byte(`<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body><nav epub:type="toc"><ol><li><a href="text/one.xhtml">Chapter One</a><ol><li><a href="text/one.xhtml#part">Part One</a></li></ol></li><li><a href="text/two.xhtml">Chapter Two</a></li></ol></nav></body></html>`)},
		{"OEBPS/text/one.xhtml", []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head><title>First document</title></head><body><p>Opening text.<img src="../images/inside.png"/><img src="../images/inside.png"/></p><h2 id="part">Part text.<img src="../images/inside.png"/></h2><p>More part text.</p><table><tr><td>Table cell one</td><td>Table cell two</td></tr></table></body></html>`)},
		{"OEBPS/text/two.xhtml", []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Second document</title></head><body><p>Second chapter body.</p></body></html>`)},
		{"OEBPS/images/cover.png", samplePNG()},
		{"OEBPS/images/inside.png", samplePNG()},
	})

	book, err := ParseEPUB(bookPath)
	if err != nil {
		t.Fatalf("ParseEPUB: %v", err)
	}
	if book.Title != "EPUB title" {
		t.Errorf("title = %q", book.Title)
	}
	if book.Author != "Author Name" {
		t.Errorf("author = %q", book.Author)
	}
	if book.CoverExt != ".png" {
		t.Errorf("cover extension = %q", book.CoverExt)
	}
	if len(book.Cover) == 0 || len(book.Cover) < 8 || string(book.Cover[1:4]) != "PNG" {
		t.Errorf("cover không được trích đúng: % x", book.Cover)
	}
	if len(book.Chapters) != 2 {
		t.Fatalf("muốn 2 chương theo navigation/spine, có %d", len(book.Chapters))
	}
	first := book.Chapters[0]
	if first.Title != "Chapter One" || len(first.Sections) != 2 {
		t.Fatalf("cấu trúc chương 1 = %#v", first)
	}
	if first.Sections[0].Title != "Chapter One" || !strings.Contains(first.Sections[0].Text, "Opening text") {
		t.Errorf("phần trước mục con sai: %#v", first.Sections[0])
	}
	if first.Sections[1].Title != "Part One" || !strings.Contains(first.Sections[1].Text, "Part text") {
		t.Errorf("mục con navigation sai: %#v", first.Sections[1])
	}
	if len(first.Sections[0].Images) != 1 || first.Sections[0].Images[0].Name != "img001.png" {
		t.Errorf("ảnh nội dung không được trích: %#v", first.Sections[0].Images)
	}
	if len(first.Sections[1].Images) != 1 || book.Stats.Images != 2 || book.Stats.Tables != 1 {
		t.Errorf("thống kê ảnh/bảng sai: stats=%+v sections=%#v", book.Stats, first.Sections)
	}
	if !strings.Contains(first.Sections[1].Text, "Table cell one") || !strings.Contains(first.Sections[1].Text, "Table cell two") {
		t.Errorf("nội dung bảng không được tách dễ đọc: %q", first.Sections[1].Text)
	}
	if book.Chapters[1].Title != "Chapter Two" || !strings.Contains(book.Chapters[1].Sections[0].Text, "Second chapter body") {
		t.Errorf("chương 2 sai: %#v", book.Chapters[1])
	}
}

func TestParseEPUB_UsesSpineWhenNavigationIsMissing(t *testing.T) {
	bookPath := writeEPUB(t, []zipEntry{
		{"META-INF/container.xml", []byte(`<container><rootfiles><rootfile full-path="EPUB/book.opf"/></rootfiles></container>`)},
		{"EPUB/book.opf", []byte(`<package><metadata><title>Fallback title</title></metadata><manifest><item id="second" href="second.xhtml" media-type="application/xhtml+xml"/><item id="first" href="first.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="first"/><itemref idref="second"/></spine></package>`)},
		{"EPUB/first.xhtml", []byte(`<html><head><title>First</title></head><body><p>First body.</p></body></html>`)},
		{"EPUB/second.xhtml", []byte(`<html><head><title>Second</title></head><body><p>Second body.</p></body></html>`)},
	})

	book, err := ParseEPUB(bookPath)
	if err != nil {
		t.Fatalf("ParseEPUB: %v", err)
	}
	if book.Title != "Fallback title" || len(book.Chapters) != 2 {
		t.Fatalf("fallback book = %#v", book)
	}
	if book.Chapters[0].Title != "First" || !strings.Contains(book.Chapters[0].Sections[0].Text, "First body") {
		t.Errorf("spine item đầu sai: %#v", book.Chapters[0])
	}
	if book.Chapters[1].Title != "Second" || !strings.Contains(book.Chapters[1].Sections[0].Text, "Second body") {
		t.Errorf("spine item thứ hai sai: %#v", book.Chapters[1])
	}
	outline, err := Inspect(bookPath, InspectOptions{})
	if err != nil || outline.FileTitle != "book" || outline.Title != "Fallback title" {
		t.Errorf("Inspect EPUB = %#v, err=%v", outline, err)
	}
	prepared, err := (Options{InputPath: bookPath, OutputDir: filepath.Join(t.TempDir(), "prepared")}).prepare()
	if err != nil || prepared.title != "Fallback title" {
		t.Errorf("prepare EPUB = %#v, err=%v", prepared, err)
	}
}

func TestParseEPUB2_NCXAndPartialNavigationPreserveSpineOrder(t *testing.T) {
	bookPath := writeEPUB(t, []zipEntry{
		{"META-INF/container.xml", []byte(`<container><rootfiles><rootfile full-path="OEBPS/book.opf"/></rootfiles></container>`)},
		{"OEBPS/book.opf", []byte(`<package><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Old EPUB</dc:title><dc:creator>Writer</dc:creator></metadata><manifest><item id="toc" href="toc.ncx" media-type="application/x-dtbncx+xml"/><item id="first" href="first.xhtml" media-type="application/xhtml+xml"/><item id="middle" href="middle.xhtml" media-type="application/xhtml+xml"/><item id="last" href="last.xhtml" media-type="application/xhtml+xml"/></manifest><spine toc="toc"><itemref idref="first"/><itemref idref="middle"/><itemref idref="last"/></spine></package>`)},
		{"OEBPS/toc.ncx", []byte(`<ncx><navMap><navPoint><navLabel><text>Middle chapter</text></navLabel><content src="middle.xhtml#start"/><navPoint><navLabel><text>Middle section</text></navLabel><content src="middle.xhtml#section"/></navPoint></navPoint></navMap></ncx>`)},
		{"OEBPS/first.xhtml", []byte(`<html><head><title>First chapter</title></head><body><p>First in spine.</p></body></html>`)},
		{"OEBPS/middle.xhtml", []byte(`<html><body><p id="start">Middle start.</p><p id="section">Middle section body.</p></body></html>`)},
		{"OEBPS/last.xhtml", []byte(`<html><head><title>Last chapter</title></head><body><p>Last in spine.</p></body></html>`)},
	})

	book, err := ParseEPUB(bookPath)
	if err != nil {
		t.Fatalf("ParseEPUB: %v", err)
	}
	if book.Author != "Writer" || len(book.Chapters) != 3 {
		t.Fatalf("EPUB 2 metadata/chapters = %#v", book)
	}
	wantTitles := []string{"First chapter", "Middle chapter", "Last chapter"}
	for i, title := range wantTitles {
		if book.Chapters[i].Title != title {
			t.Errorf("chapter %d title = %q, muốn %q", i, book.Chapters[i].Title, title)
		}
	}
	if len(book.Chapters[1].Sections) != 2 || !strings.Contains(book.Chapters[1].Sections[1].Text, "Middle section body") {
		t.Errorf("NCX section không được tách: %#v", book.Chapters[1])
	}
	outline, err := Inspect(bookPath, InspectOptions{})
	if err != nil || outline.Author != "Writer" {
		t.Errorf("Inspect author = %q, err=%v", outline.Author, err)
	}
	prepared, err := (Options{InputPath: bookPath, OutputDir: filepath.Join(t.TempDir(), "prepared")}).prepare()
	if err != nil || prepared.meta.Author != "Writer" {
		t.Errorf("prepared author = %q, err=%v", prepared.meta.Author, err)
	}
}

func TestParseEPUBNavigationGroupWithoutHref(t *testing.T) {
	bookPath := writeEPUB(t, []zipEntry{
		{"META-INF/container.xml", []byte(`<container><rootfiles><rootfile full-path="OEBPS/book.opf"/></rootfiles></container>`)},
		{"OEBPS/book.opf", []byte(`<package><manifest><item id="nav" href="nav.xhtml" properties="nav"/><item id="text" href="text.xhtml"/></manifest><spine><itemref idref="text"/></spine></package>`)},
		{"OEBPS/nav.xhtml", []byte(`<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body><nav epub:type="toc"><ol><li><span>Part group</span><ol><li><a href="text.xhtml">Linked section</a></li></ol></li></ol></nav></body></html>`)},
		{"OEBPS/text.xhtml", []byte(`<html><body><p>Book text.</p></body></html>`)},
	})
	reader, err := zip.OpenReader(bookPath)
	if err != nil {
		t.Fatal(err)
	}
	entries := make(map[string]*zip.File, len(reader.File))
	for _, entry := range reader.File {
		entries[entry.Name] = entry
	}
	nav, err := parseEPUBNav(&reader.Reader, entries, "OEBPS/nav.xhtml")
	_ = reader.Close()
	if err != nil || len(nav) != 1 || nav[0].title != "Part group" || len(nav[0].children) != 1 {
		t.Fatalf("navigation group parse = %#v, err=%v", nav, err)
	}
	book, err := ParseEPUB(bookPath)
	if err != nil {
		t.Fatalf("ParseEPUB: %v", err)
	}
	if len(book.Chapters) != 1 || book.Chapters[0].Title != "Part group" || len(book.Chapters[0].Sections) != 1 {
		t.Fatalf("nhóm navigation không href bị mất: %#v", book.Chapters)
	}
	if book.Chapters[0].Sections[0].Title != "Linked section" || !strings.Contains(book.Chapters[0].Sections[0].Text, "Book text") {
		t.Errorf("section trong nhóm navigation sai: %#v", book.Chapters[0].Sections[0])
	}
}

func TestParseEPUB_RejectsTraversalResource(t *testing.T) {
	bookPath := writeEPUB(t, []zipEntry{
		{"META-INF/container.xml", []byte(`<container><rootfiles><rootfile full-path="../book.opf"/></rootfiles></container>`)},
	})
	if _, err := ParseEPUB(bookPath); err == nil {
		t.Fatal("ParseEPUB phải từ chối rootfile thoát khỏi archive")
	}
}

func TestParseEPUB_RejectsExcessiveSpineText(t *testing.T) {
	oldLimit := maxEPUBTextBytes
	maxEPUBTextBytes = 1
	t.Cleanup(func() { maxEPUBTextBytes = oldLimit })
	bookPath := writeEPUB(t, []zipEntry{
		{"META-INF/container.xml", []byte(`<container><rootfiles><rootfile full-path="book.opf"/></rootfiles></container>`)},
		{"book.opf", []byte(`<package><manifest><item id="text" href="text.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="text"/></spine></package>`)},
		{"text.xhtml", []byte(`<html><body><p>Text.</p></body></html>`)},
	})
	if _, err := ParseEPUB(bookPath); err == nil {
		t.Fatal("nội dung spine vượt giới hạn phải bị từ chối")
	}
}

func TestParseSourceAcceptsOnlySupportedExtensions(t *testing.T) {
	if !IsSupportedSourcePath("BOOK.EPUB") || !IsSupportedSourcePath("book.docx") || IsSupportedSourcePath("book.pdf") {
		t.Fatal("nhận diện phần mở rộng nguồn sai")
	}
	if _, err := ParseSource("book.pdf"); err == nil {
		t.Fatal("ParseSource phải từ chối định dạng không hỗ trợ")
	}
}

func TestWriteCoverWithSource_UsesEmbeddedEPUBCover(t *testing.T) {
	dir := t.TempDir()
	data := samplePNG()
	name, err := (Options{OutputDir: dir}).writeCoverWithSource(nil, "Book", data, ".png")
	if err != nil {
		t.Fatalf("writeCoverWithSource: %v", err)
	}
	if name != "cover.png" {
		t.Fatalf("cover name = %q", name)
	}
	written, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(written) != string(data) {
		t.Errorf("embedded cover output differs: err=%v", err)
	}
}

func TestReadEPUBEntryHonorsSizeLimit(t *testing.T) {
	oldLimit := maxXMLBytes
	maxXMLBytes = 5
	t.Cleanup(func() { maxXMLBytes = oldLimit })
	file, err := os.Create(filepath.Join(t.TempDir(), "large.epub"))
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("large.xhtml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("123456")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	entries := map[string]*zip.File{reader.File[0].Name: reader.File[0]}
	if _, err := readEPUBEntry(&reader.Reader, entries, "large.xhtml"); err == nil {
		t.Fatal("entry lớn hơn giới hạn phải bị từ chối")
	}
}
