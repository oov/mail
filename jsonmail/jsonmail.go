package jsonmail

import (
	"bytes"
	gonethtml "code.google.com/p/go.net/html"
	"code.google.com/p/go.net/html/charset"
	"encoding/base64"
	"fmt"
	"github.com/oov/mail"
	"html"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
)

type JSONMail struct {
	Header      textproto.MIMEHeader
	IsText      bool
	Body        string
	IsMultipart bool
	Error       string
	Children    []*JSONMail
	parent      *JSONMail
}

func Parse(msg *mail.Message) (*JSONMail, error) {
	var jsmRoot JSONMail
	if err := parseMessage(&jsmRoot, msg.Body, textproto.MIMEHeader(msg.Header), 0); err != nil {
		return nil, err
	}
	return jsmRoot.Children[0], nil
}

func (m *JSONMail) FindTextBody() (body string, convertedFromHTML bool, err error) {
	if !m.IsMultipart {
		if m.IsText {
			body, convertedFromHTML = extractByText(m)
		}
		return
	}

	q := []*JSONMail{m}
	for qp, n := q[:], 0; len(qp) != 0; qp, n = q[len(q)-n:], 0 {
		for _, msg := range qp {
			for _, cld := range msg.Children {
				q = append(q, cld)
				n++
			}
		}
	}

	var text, html *JSONMail
	for _, p := range q {
		if !p.IsMultipart && p.IsText && p.Header.Get("Content-Disposition") == "" {
			switch ct, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type")); strings.ToLower(ct) {
			case "text/plain":
				if text == nil {
					text = p
				}
			case "text/html":
				if html == nil {
					html = p
				}
			}
		}
	}
	if text != nil {
		body, convertedFromHTML = extractByText(text)
		return
	}
	if html != nil {
		body, convertedFromHTML = extractByText(html)
		return
	}

	err = fmt.Errorf("could not find valid text part")
	return
}

func getNodeText(node *gonethtml.Node) string {
	if node.Type == gonethtml.TextNode {
		return node.Data
	} else if node.FirstChild != nil {
		var buf bytes.Buffer
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == gonethtml.ElementNode && (c.Data == "script" || c.Data == "style") {
				continue
			}
			buf.WriteString(getNodeText(c))
		}
		return buf.String()
	}
	return ""
}

func extractByText(m *JSONMail) (body string, convertedFromHTML bool) {
	switch t, _, _ := mime.ParseMediaType(m.Header.Get("Content-Type")); strings.ToLower(t) {
	case "text/html":
		n, err := gonethtml.Parse(bytes.NewBufferString(m.Body))
		if err != nil {
			return m.Body, false
		}
		return getNodeText(n), true
	default:
		return m.Body, false
	}
}

func extractByHTML(m *JSONMail) string {
	switch t, _, _ := mime.ParseMediaType(m.Header.Get("Content-Type")); strings.ToLower(t) {
	case "text/html":
		// image filename reformat
		return m.Body
	default:
		return html.EscapeString(m.Body)
	}
}

func parseMessage(parent *JSONMail, body io.Reader, header textproto.MIMEHeader, depth int) error {
	jsm := &JSONMail{
		parent: parent,
		Header: make(textproto.MIMEHeader),
	}
	parent.Children = append(parent.Children, jsm)
	for k, v := range header {
		vs := make([]string, len(v))
		for i, l := range v {
			vs[i] = mail.DecodeRFC2047(l)
		}
		jsm.Header[k] = vs
	}

	contentType := header.Get("Content-Type")
	jsm.IsMultipart = len(contentType) > 10 && strings.ToLower(contentType[:10]) == "multipart/"
	if !jsm.IsMultipart {
		r, err := getReader(body, header)
		if err != nil {
			jsm.Error = err.Error()
			if r == nil {
				return err
			}
		}

		b := bytes.NewBufferString("")
		jsm.IsText = len(contentType) > 5 && strings.ToLower(contentType[:5]) == "text/"
		if jsm.IsText {
			_, err = io.Copy(b, r)
		} else {
			_, err = io.Copy(base64.NewEncoder(base64.StdEncoding, b), r)
		}
		if err != nil {
			jsm.Error = err.Error()
			return err
		}
		jsm.Body = b.String()
		return nil
	}

	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		jsm.Error = err.Error()
		return err
	}

	boundary, ok := params["boundary"]
	if !ok {
		err = fmt.Errorf("invalid multiparted Content-Type(boundary missing): %v", contentType)
		jsm.Error = err.Error()
		return err
	}

	mpr := multipart.NewReader(body, boundary)
	var p *multipart.Part
	for p, err = mpr.NextPart(); err == nil; p, err = mpr.NextPart() {
		if err = parseMessage(jsm, p, p.Header, depth+1); err != nil {
			log.Println(err)
			continue
		}
	}
	if err != io.EOF {
		jsm.Error = err.Error()
		return err
	}
	return nil
}

func getReader(r io.Reader, h textproto.MIMEHeader) (io.Reader, error) {
	var err error
	r, err = mail.TransferEncodingDecoder(r, h.Get("Content-Transfer-Encoding"))
	if err != nil {
		return nil, err
	}

	ct := h.Get("Content-Type")
	if len(ct) > 5 && strings.ToLower(ct[:5]) == "text/" {
		r2, err := charset.NewReader(r, ct)
		if err != nil {
			return r, err // return original reader
		}
		return r2, nil
	}
	return r, nil
}
