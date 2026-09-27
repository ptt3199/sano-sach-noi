package bookmaker

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"sort"
	"strings"
)

type epubContainer struct {
	Rootfiles []struct {
		FullPath string `xml:"full-path,attr"`
	} `xml:"rootfiles>rootfile"`
}

type epubPackage struct {
	Metadata struct {
		Titles   []string `xml:"title"`
		Creators []string `xml:"creator"`
		Meta     []struct {
			Name     string `xml:"name,attr"`
			Content  string `xml:"content,attr"`
			Property string `xml:"property,attr"`
			Value    string `xml:",chardata"`
		} `xml:"meta"`
	} `xml:"metadata"`
	Manifest struct {
		Items []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			MediaType  string `xml:"media-type,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		TOC   string `xml:"toc,attr"`
		Items []struct {
			IDRef  string `xml:"idref,attr"`
			Linear string `xml:"linear,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
}

type epubManifestItem struct {
	id         string
	path       string
	mediaType  string
	properties string
}

type epubNavEntry struct {
	title    string
	href     string
	path     string
	fragment string
	children []epubNavEntry
}

type epubNavList struct {
	Items []epubNavEntry `xml:"li"`
}

func (list *epubNavList) UnmarshalXML(dec *xml.Decoder, start xml.StartElement) error {
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch value := tok.(type) {
		case xml.StartElement:
			if value.Name.Local == "li" {
				var item epubNavEntry
				if err := dec.DecodeElement(&item, &value); err != nil {
					return err
				}
				list.Items = append(list.Items, item)
			}
		case xml.EndElement:
			if value.Name == start.Name {
				return nil
			}
		}
	}
}

func (item *epubNavEntry) UnmarshalXML(dec *xml.Decoder, start xml.StartElement) error {
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch value := tok.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "a", "span":
				label, href, err := decodeEPUBNavLabel(dec, value)
				if err != nil {
					return err
				}
				if item.title == "" {
					item.title = strings.TrimSpace(label)
				}
				if item.href == "" {
					item.href = href
				}
			case "ol":
				var nested epubNavList
				if err := dec.DecodeElement(&nested, &value); err != nil {
					return err
				}
				item.children = append(item.children, nested.Items...)
			}
		case xml.EndElement:
			if value.Name == start.Name {
				return nil
			}
		case xml.CharData:
			if item.title == "" {
				item.title = strings.TrimSpace(string(value))
			}
		}
	}
}

func decodeEPUBNavLabel(dec *xml.Decoder, start xml.StartElement) (string, string, error) {
	href := ""
	for _, attr := range start.Attr {
		if attr.Name.Local == "href" {
			href = attr.Value
			break
		}
	}
	var text strings.Builder
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return "", "", err
		}
		switch value := tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			text.Write(value)
		}
	}
	return text.String(), href, nil
}

type epubNCX struct {
	NavMap struct {
		Points []epubNCXPoint `xml:"navPoint"`
	} `xml:"navMap"`
}

type epubNCXPoint struct {
	NavLabel struct {
		Text string `xml:"text"`
	} `xml:"navLabel"`
	Content struct {
		Src string `xml:"src,attr"`
	} `xml:"content"`
	Children []epubNCXPoint `xml:"navPoint"`
}

type epubContentBlock struct {
	text    string
	heading int
	images  []string
}

type epubDocument struct {
	path   string
	title  string
	blocks []epubContentBlock
	ids    map[string]int
	tables int
}

var maxEPUBTextBytes int64 = 256 << 20

// ParseEPUB reads an EPUB archive and maps its metadata and reading order to Book.
func ParseEPUB(filePath string) (*Book, error) {
	zr, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, fmt.Errorf("mở epub %q: %w", filePath, err)
	}
	defer func() { _ = zr.Close() }()
	return parseEPUB(&zr.Reader)
}

func parseEPUB(zr *zip.Reader) (*Book, error) {
	entries := make(map[string]*zip.File, len(zr.File))
	for _, file := range zr.File {
		if _, exists := entries[file.Name]; exists {
			return nil, fmt.Errorf("epub có entry trùng tên %q", file.Name)
		}
		entries[file.Name] = file
	}
	containerData, err := readEPUBEntry(zr, entries, "META-INF/container.xml")
	if err != nil {
		return nil, fmt.Errorf("đọc META-INF/container.xml: %w", err)
	}
	var container epubContainer
	if err := xml.Unmarshal(containerData, &container); err != nil {
		return nil, fmt.Errorf("phân tích container.xml: %w", err)
	}
	if len(container.Rootfiles) == 0 {
		return nil, fmt.Errorf("epub thiếu rootfile trong container.xml")
	}
	opfPath, err := cleanEPUBPath("", container.Rootfiles[0].FullPath)
	if err != nil {
		return nil, fmt.Errorf("đường dẫn package EPUB không hợp lệ: %w", err)
	}
	opfData, err := readEPUBEntry(zr, entries, opfPath)
	if err != nil {
		return nil, fmt.Errorf("đọc package EPUB: %w", err)
	}
	var pkg epubPackage
	if err := xml.Unmarshal(opfData, &pkg); err != nil {
		return nil, fmt.Errorf("phân tích package EPUB: %w", err)
	}

	manifest := make(map[string]epubManifestItem, len(pkg.Manifest.Items))
	for _, item := range pkg.Manifest.Items {
		itemPath, err := cleanEPUBPath(opfPath, item.Href)
		if err != nil {
			return nil, fmt.Errorf("đường dẫn resource EPUB %q không hợp lệ: %w", item.Href, err)
		}
		manifest[item.ID] = epubManifestItem{
			id: item.ID, path: itemPath, mediaType: item.MediaType, properties: item.Properties,
		}
	}

	var spine []epubManifestItem
	spineRank := make(map[string]int)
	for _, ref := range pkg.Spine.Items {
		if ref.Linear == "no" {
			continue
		}
		item, ok := manifest[ref.IDRef]
		if !ok {
			continue
		}
		if _, exists := entries[item.path]; !exists {
			return nil, fmt.Errorf("spine EPUB tham chiếu resource không tồn tại %q", item.path)
		}
		spineRank[item.path] = len(spine)
		spine = append(spine, item)
	}
	if len(spine) == 0 {
		return nil, fmt.Errorf("epub không có nội dung tuyến tính trong spine")
	}

	book := &Book{}
	if len(pkg.Metadata.Titles) > 0 {
		book.Title = strings.TrimSpace(pkg.Metadata.Titles[0])
	}
	if len(pkg.Metadata.Creators) > 0 {
		book.Author = strings.TrimSpace(pkg.Metadata.Creators[0])
	}

	coverID := ""
	for _, meta := range pkg.Metadata.Meta {
		if strings.EqualFold(meta.Name, "cover") {
			coverID = meta.Content
		}
	}
	for _, manifestEntry := range pkg.Manifest.Items {
		if hasEPUBProperty(manifestEntry.Properties, "cover-image") {
			coverID = manifestEntry.ID
			break
		}
	}
	coverItem := manifest[coverID]

	var tocItem *epubManifestItem
	if item, ok := manifest[pkg.Spine.TOC]; ok {
		tocItem = &item
	}
	if tocItem == nil {
		for _, manifestEntry := range pkg.Manifest.Items {
			item := manifest[manifestEntry.ID]
			if item.mediaType == "application/x-dtbncx+xml" {
				tocItem = &item
				break
			}
		}
	}
	var navItem *epubManifestItem
	for _, manifestEntry := range pkg.Manifest.Items {
		item := manifest[manifestEntry.ID]
		if hasEPUBProperty(item.properties, "nav") {
			navItem = &item
			break
		}
	}

	var nav []epubNavEntry
	navBase := ""
	if navItem != nil {
		nav, _ = parseEPUBNav(zr, entries, navItem.path)
		navBase = navItem.path
	}
	if len(nav) == 0 && tocItem != nil {
		nav, _ = parseEPUBNCX(zr, entries, tocItem.path)
		navBase = tocItem.path
	}
	normalizeEPUBNav(nav, navBase)

	documents := make(map[string]*epubDocument, len(spine))
	var totalTextBytes int64
	for _, item := range spine {
		totalTextBytes += int64(entries[item.path].UncompressedSize64)
		if totalTextBytes > maxEPUBTextBytes {
			return nil, fmt.Errorf("nội dung EPUB vượt giới hạn %d MB sau khi giải nén", maxEPUBTextBytes>>20)
		}
		data, err := readEPUBEntry(zr, entries, item.path)
		if err != nil {
			return nil, fmt.Errorf("đọc nội dung EPUB %q: %w", item.path, err)
		}
		doc, err := parseEPUBDocument(item.path, data)
		if err != nil {
			return nil, fmt.Errorf("phân tích nội dung EPUB %q: %w", item.path, err)
		}
		documents[item.path] = doc
		book.Stats.Tables += doc.tables
		if book.Title == "" && doc.title != "" {
			book.Title = doc.title
		}
	}

	loadImage := newEPUBImageLoader(zr, entries, book)
	if coverItem.path != "" {
		if data, err := readZipFile(zr, coverItem.path, maxImageBytes); err == nil {
			ext := strings.ToLower(path.Ext(coverItem.path))
			if ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" {
				book.Cover, book.CoverExt = data, ext
			}
		}
	}
	if len(nav) == 0 {
		for _, item := range spine {
			doc := documents[item.path]
			chapterTitle := firstNonEmpty(doc.title, strings.TrimSuffix(path.Base(item.path), path.Ext(item.path)), "Nội dung")
			chapter := Chapter{Title: chapterTitle}
			section := epubSectionFromBlocks(doc, 0, len(doc.blocks), chapterTitle, loadImage)
			if strings.TrimSpace(section.Text) != "" || len(section.Images) > 0 {
				chapter.Sections = append(chapter.Sections, section)
			}
			if len(chapter.Sections) > 0 {
				book.Chapters = append(book.Chapters, chapter)
			}
		}
	} else {
		appendEPUBNavigation(book, nav, documents, spineRank, loadImage)
	}
	assignStems(book)
	if len(book.Chapters) == 0 {
		return nil, fmt.Errorf("epub không có nội dung văn bản nào")
	}
	return book, nil
}

func readEPUBEntry(zr *zip.Reader, entries map[string]*zip.File, name string) ([]byte, error) {
	if name == "" || strings.HasPrefix(name, "../") || name == ".." || strings.HasPrefix(name, "/") {
		return nil, fmt.Errorf("đường dẫn entry không an toàn %q", name)
	}
	if entries[name] == nil {
		return nil, fmt.Errorf("không tìm thấy entry %q", name)
	}
	return readZipFile(zr, name, maxXMLBytes)
}

func cleanEPUBPath(baseFile, href string) (string, error) {
	if strings.Contains(href, `\`) {
		return "", fmt.Errorf("đường dẫn chứa dấu gạch chéo ngược")
	}
	u, err := url.Parse(href)
	if err != nil {
		return "", err
	}
	if u.Scheme != "" || u.Host != "" {
		return "", fmt.Errorf("URI ngoài archive không được hỗ trợ")
	}
	name := u.Path
	if strings.HasPrefix(name, "/") {
		name = strings.TrimPrefix(name, "/")
	} else if baseFile != "" {
		name = path.Join(path.Dir(baseFile), name)
	}
	name = path.Clean(name)
	if name == "." || name == ".." || strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("đường dẫn vượt khỏi archive")
	}
	return name, nil
}

func hasEPUBProperty(properties, wanted string) bool {
	for _, value := range strings.Fields(properties) {
		if value == wanted {
			return true
		}
	}
	return false
}

func normalizeEPUBNav(entries []epubNavEntry, base string) {
	for i := range entries {
		if strings.TrimSpace(entries[i].href) == "" {
			normalizeEPUBNav(entries[i].children, base)
			continue
		}
		if resolved, fragment, err := resolveEPUBHref(base, entries[i].href); err == nil {
			entries[i].href = (&url.URL{Path: resolved, Fragment: fragment}).String()
		} else {
			entries[i].href = ""
		}
		normalizeEPUBNav(entries[i].children, base)
	}
}

func parseEPUBNav(zr *zip.Reader, entries map[string]*zip.File, name string) ([]epubNavEntry, error) {
	data, err := readEPUBEntry(zr, entries, name)
	if err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "nav" || !isEPUBTOCNav(start) {
			continue
		}
		var nav struct {
			Lists []epubNavList `xml:"ol"`
		}
		if err := dec.DecodeElement(&nav, &start); err != nil {
			return nil, err
		}
		if len(nav.Lists) > 0 {
			return nav.Lists[0].Items, nil
		}
	}
	return nil, nil
}

func isEPUBTOCNav(start xml.StartElement) bool {
	for _, attr := range start.Attr {
		if attr.Name.Local == "type" && strings.Contains(attr.Value, "toc") {
			return true
		}
	}
	return false
}

func parseEPUBNCX(zr *zip.Reader, entries map[string]*zip.File, name string) ([]epubNavEntry, error) {
	data, err := readEPUBEntry(zr, entries, name)
	if err != nil {
		return nil, err
	}
	var ncx epubNCX
	if err := xml.Unmarshal(data, &ncx); err != nil {
		return nil, err
	}
	var convert func([]epubNCXPoint) []epubNavEntry
	convert = func(points []epubNCXPoint) []epubNavEntry {
		out := make([]epubNavEntry, 0, len(points))
		for _, point := range points {
			out = append(out, epubNavEntry{
				title: strings.TrimSpace(point.NavLabel.Text), href: point.Content.Src,
				children: convert(point.Children),
			})
		}
		return out
	}
	return convert(ncx.NavMap.Points), nil
}

func parseEPUBDocument(name string, data []byte) (*epubDocument, error) {
	doc := &epubDocument{path: name, ids: make(map[string]int)}
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	var current *epubContentBlock
	inBody := false
	skipDepth := 0
	var titleDepth int
	var titleText strings.Builder
	flush := func() {
		if current == nil {
			return
		}
		current.text = strings.TrimSpace(current.text)
		if current.text != "" || len(current.images) > 0 {
			doc.blocks = append(doc.blocks, *current)
		}
		current = nil
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch value := tok.(type) {
		case xml.StartElement:
			local := strings.ToLower(value.Name.Local)
			if local == "table" && inBody && skipDepth == 0 {
				doc.tables++
			}
			if local == "title" && !inBody {
				titleDepth = 1
			} else if titleDepth > 0 {
				titleDepth++
			}
			if local == "body" {
				inBody = true
				continue
			}
			if !inBody {
				continue
			}
			if skipDepth > 0 {
				skipDepth++
				continue
			}
			if local == "nav" || local == "script" || local == "style" {
				skipDepth = 1
				continue
			}
			level := epubHeadingLevel(local)
			if isEPUBBlock(local) {
				flush()
				current = &epubContentBlock{heading: level}
			}
			if current == nil {
				current = &epubContentBlock{}
			}
			for _, attr := range value.Attr {
				if attr.Name.Local == "id" && attr.Value != "" {
					doc.ids[attr.Value] = len(doc.blocks)
				}
				if local == "img" && attr.Name.Local == "src" && attr.Value != "" {
					current.images = append(current.images, attr.Value)
				}
			}
			if local == "br" {
				current.text += "\n"
			}
		case xml.EndElement:
			local := strings.ToLower(value.Name.Local)
			if titleDepth > 0 {
				titleDepth--
				if titleDepth == 0 {
					doc.title = strings.TrimSpace(titleText.String())
					titleText.Reset()
				}
			}
			if local == "body" {
				flush()
				inBody = false
				continue
			}
			if skipDepth > 0 {
				skipDepth--
				continue
			}
			if inBody && isEPUBBlock(local) {
				flush()
			}
		case xml.CharData:
			if titleDepth > 0 {
				titleText.Write(value)
			}
			if !inBody || skipDepth > 0 {
				continue
			}
			text := string(value)
			if strings.TrimSpace(text) == "" && current == nil {
				continue
			}
			if current == nil {
				current = &epubContentBlock{}
			}
			current.text += text
		}
	}
	flush()
	return doc, nil
}

func isEPUBBlock(name string) bool {
	return name == "p" || name == "li" || name == "td" || name == "th" || name == "blockquote" || name == "pre" || epubHeadingLevel(name) > 0
}

func epubHeadingLevel(name string) int {
	if len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6' {
		return int(name[1] - '0')
	}
	return 0
}

func epubSectionFromBlocks(doc *epubDocument, start, end int, title string, loadImage func(string, string) (SectionImage, bool)) Section {
	section := Section{Title: strings.TrimSpace(title)}
	seenImages := make(map[string]bool)
	for i := start; i < end && i < len(doc.blocks); i++ {
		block := doc.blocks[i]
		if block.text != "" {
			if section.Text != "" {
				section.Text += "\n\n"
			}
			section.Text += block.text
		}
		for _, src := range block.images {
			imagePath, _, err := resolveEPUBHref(doc.path, src)
			if err != nil || seenImages[imagePath] {
				continue
			}
			seenImages[imagePath] = true
			if image, ok := loadImage(doc.path, src); ok {
				section.Images = append(section.Images, image)
			}
		}
	}
	return section
}

func appendEPUBNavigation(book *Book, nav []epubNavEntry, documents map[string]*epubDocument, spineRank map[string]int, loadImage func(string, string) (SectionImage, bool)) {
	flatten := func(root epubNavEntry) []epubNavEntry {
		var entries []epubNavEntry
		var visit func(epubNavEntry)
		visit = func(item epubNavEntry) {
			entries = append(entries, item)
			for _, child := range item.children {
				visit(child)
			}
		}
		visit(root)
		return entries
	}
	// Navigation roots are kept in document order, with their nested entries
	// flattened into the section structure supported by Book.
	type navRoot struct {
		entry epubNavEntry
		rank  int
		pos   int
	}
	roots := make([]navRoot, 0, len(nav))
	for _, root := range nav {
		resolved, fragment, err := resolveEPUBHref("", firstEPUBNavHref(root))
		if err != nil {
			continue
		}
		root.path, root.fragment = resolved, fragment
		rank, ok := spineRank[resolved]
		if !ok {
			continue
		}
		pos := 0
		if fragment != "" {
			pos = documents[resolved].ids[fragment]
		}
		roots = append(roots, navRoot{entry: root, rank: rank, pos: pos})
	}
	navCovered := make(map[string]bool)
	for _, root := range nav {
		for _, entry := range flatten(root) {
			resolved, _, err := resolveEPUBHref("", entry.href)
			if err == nil && documents[resolved] != nil {
				navCovered[resolved] = true
			}
		}
	}
	for name, doc := range documents {
		if navCovered[name] {
			continue
		}
		title := firstNonEmpty(doc.title, strings.TrimSuffix(path.Base(name), path.Ext(name)), "Nội dung")
		roots = append(roots, navRoot{entry: epubNavEntry{title: title, href: name}, rank: spineRank[name]})
	}
	sort.SliceStable(roots, func(i, j int) bool {
		if roots[i].rank == roots[j].rank {
			return roots[i].pos < roots[j].pos
		}
		return roots[i].rank < roots[j].rank
	})
	for _, candidate := range roots {
		root := candidate.entry
		entries := flatten(root)
		type target struct {
			entry epubNavEntry
			doc   *epubDocument
			start int
			rank  int
			order int
		}
		targets := make([]target, 0, len(entries))
		for order, entry := range entries {
			resolved, fragment, err := resolveEPUBHref("", entry.href)
			if err != nil {
				continue
			}
			doc := documents[resolved]
			if doc == nil {
				continue
			}
			start := 0
			if fragment != "" {
				start = doc.ids[fragment]
			}
			targets = append(targets, target{entry: entry, doc: doc, start: start, rank: spineRank[resolved], order: order})
		}
		sort.SliceStable(targets, func(i, j int) bool {
			if targets[i].rank == targets[j].rank {
				if targets[i].start == targets[j].start {
					return targets[i].order < targets[j].order
				}
				return targets[i].start < targets[j].start
			}
			return targets[i].rank < targets[j].rank
		})
		chapter := Chapter{Title: firstNonEmpty(root.title, "Nội dung")}
		for i, target := range targets {
			end := len(target.doc.blocks)
			for j := i + 1; j < len(targets); j++ {
				if targets[j].doc.path == target.doc.path && targets[j].start > target.start {
					end = targets[j].start
					break
				}
			}
			if i > 0 && targets[i-1].doc.path == target.doc.path && targets[i-1].start == target.start {
				continue
			}
			section := epubSectionFromBlocks(target.doc, target.start, end, target.entry.title, loadImage)
			if strings.TrimSpace(section.Text) != "" || len(section.Images) > 0 {
				chapter.Sections = append(chapter.Sections, section)
			}
		}
		if len(chapter.Sections) > 0 {
			book.Chapters = append(book.Chapters, chapter)
		}
	}
}

func firstEPUBNavHref(entry epubNavEntry) string {
	if entry.href != "" {
		return entry.href
	}
	for _, child := range entry.children {
		if href := firstEPUBNavHref(child); href != "" {
			return href
		}
	}
	return ""
}

func resolveEPUBHref(baseFile, href string) (string, string, error) {
	if strings.Contains(href, `\`) {
		return "", "", fmt.Errorf("href chứa dấu gạch chéo ngược")
	}
	u, err := url.Parse(href)
	if err != nil {
		return "", "", err
	}
	if u.Scheme != "" || u.Host != "" {
		return "", "", fmt.Errorf("href ngoài archive không được hỗ trợ")
	}
	name, err := cleanEPUBPath(baseFile, href)
	if err != nil {
		return "", "", err
	}
	return name, u.Fragment, nil
}

func newEPUBImageLoader(zr *zip.Reader, entries map[string]*zip.File, book *Book) func(string, string) (SectionImage, bool) {
	dataByPath := make(map[string][]byte)
	nameByPath := make(map[string]string)
	var total int64
	refs := 0
	return func(docPath, src string) (SectionImage, bool) {
		imagePath, _, err := resolveEPUBHref(docPath, src)
		if err != nil || entries[imagePath] == nil {
			return SectionImage{}, false
		}
		if refs >= maxImageRefs {
			book.Stats.DroppedImages++
			return SectionImage{}, false
		}
		refs++
		if data, ok := dataByPath[imagePath]; ok {
			if data != nil {
				book.Stats.Images++
			}
			return SectionImage{RelID: imagePath, Name: nameByPath[imagePath], Data: data}, data != nil
		}
		limit := min(maxImageBytes, maxTotalImageBytes-total)
		data, err := readZipFile(zr, imagePath, limit)
		if errors.Is(err, errZipEntryTooLarge) {
			book.Stats.SkippedImages++
		}
		if err != nil {
			dataByPath[imagePath] = nil
			return SectionImage{}, false
		}
		name := safeImageName(len(nameByPath)+1, imagePath)
		dataByPath[imagePath], nameByPath[imagePath] = data, name
		total += int64(len(data))
		book.Stats.Images++
		return SectionImage{RelID: imagePath, Name: name, Data: data}, true
	}
}
