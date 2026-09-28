package web

import (
	"io"
	"net/http"
	"strings"
)

// Sent is what a form sent under param, Rails-style ("pillar[title]" is
// Sent(r, "pillar")["title"]), from an urlencoded or a multipart body. A
// field the form didn't send isn't in it, so overlaying it on a record's
// values keeps the rest, as Rails' permitted params do.
func Sent(r *http.Request, param string) map[string]string {
	out := map[string]string{}
	prefix := param + "["
	collect := func(values map[string][]string) {
		for key, v := range values {
			// The last value, as Rails: a checkbox sends a hidden "0" and
			// then, when ticked, its "1".
			if strings.HasPrefix(key, prefix) && strings.HasSuffix(key, "]") && len(v) > 0 {
				out[key[len(prefix):len(key)-1]] = v[len(v)-1]
			}
		}
	}
	r.ParseForm()
	collect(r.PostForm)
	if r.MultipartForm != nil {
		collect(r.MultipartForm.Value)
	}
	return out
}

// Upload is a file field's file, if one was chosen: its name and bytes.
func Upload(r *http.Request, name string) (filename string, data []byte, ok bool) {
	file, header, err := r.FormFile(name)
	if err != nil {
		return "", nil, false
	}
	defer file.Close()
	data, err = io.ReadAll(file)
	if err != nil || len(data) == 0 {
		return "", nil, false
	}
	return header.Filename, data, true
}

// Sentence joins messages as Rails' to_sentence: "a", "a and b", "a, b,
// and c". A form's errors are shown as one.
func Sentence(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
}
