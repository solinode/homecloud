package lambda

import "testing"

func TestMatchPath(t *testing.T) {
	cases := []struct {
		tmpl, path string
		ok         bool
		params     map[string]string
	}{
		{"/items/{id}", "/items/42", true, map[string]string{"id": "42"}},
		{"/items/{id}", "/items/", false, nil},
		{"/items/{id}", "/items/42/extra", false, nil},
		{"/files/{proxy+}", "/files/a/b/c.txt", true, map[string]string{"proxy": "a/b/c.txt"}},
		{"/", "/", true, map[string]string{}},
		{"/users/{u}/posts/{p}", "/users/7/posts/9", true, map[string]string{"u": "7", "p": "9"}},
	}
	for _, c := range cases {
		p, ok := matchPath(c.tmpl, c.path)
		if ok != c.ok {
			t.Errorf("%s vs %s: ok=%v", c.tmpl, c.path, ok)
			continue
		}
		for k, v := range c.params {
			if p[k] != v {
				t.Errorf("%s vs %s: %s=%q want %q", c.tmpl, c.path, k, p[k], v)
			}
		}
	}
	if specificity(Route{Method: "GET", Path: "/items/new"}) <= specificity(Route{Method: "GET", Path: "/items/{id}"}) {
		t.Error("literal routes must win over parameters")
	}
}
