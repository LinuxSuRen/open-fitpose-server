// Package webapp 内嵌前端静态资源，随二进制一起分发。
package webapp

import "embed"

//go:embed index.html app.js style.css
var FS embed.FS
