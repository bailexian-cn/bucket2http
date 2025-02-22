package main

import (
	"context"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// HTML 目录列表模板
const dirListTemplate = `
<!DOCTYPE html>
<html>
<head>
    <title>Index of {{.Path}}</title>
    <style>
        body {
            font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif;
            margin: 20px;
            font-size: 13px;
            color: #333;
        }
		.icon {
			width: 16px;
			height: 16px;
			vertical-align: middle;
			margin-right: 5px;
		}			
        h1 {
            font-size: 15px;
            margin: 0 0 12px 0;
            padding-bottom: 5px;
            border-bottom: 1px solid #eee;
        }
        table {
            border-collapse: collapse;
            width: 100%;
            line-height: 1.4;
        }
        th {
            text-align: left;
            padding: 4px 8px;
            background-color: #f8f9fa;
            border-bottom: 2px solid #ddd;
            font-weight: 500;
        }
        td {
            padding: 3px 8px;
            border-bottom: 1px solid #eee;
        }
        .folder {
            font-weight: 500;
        }
        a {
            text-decoration: none;
            color: #0366d6;
        }
        a:hover {
            text-decoration: underline;
        }
    </style>
</head>
<body>
    <h1>Index of {{.Path}}</h1>
    <table>
        <tr><th>Name</th><th>Size</th><th>Last Modified</th></tr>
        {{range .Entries}}
        <tr>
            <td>
                {{.Icon}}
                <a href="{{.URL}}" class="{{if .IsDir}}folder{{end}}">
                    {{.Name}}{{if .IsDir}}/{{end}}
                </a>
            </td>
            <td>{{.Size}}</td>
            <td>{{.ModTime.Format "2006-01-02 15:04:05"}}</td>
        </tr>
        {{end}}
    </table>
</body>
</html>`

var (
	minioClient *minio.Client
	address     = *flag.String("address", ":80", "The endpoint of service")
	bucket      = *flag.String("bucket", "mirror", "The bucket of oss")
	endpoint    = *flag.String("endpoint", "192.168.31.12:9000", "The endpoint of oss")
	accessKey   = *flag.String("access-key", "bailexian", "The access key of oss")
	secretKey   = *flag.String("secret-key", "bailexian_kakoi", "The secret key of oss")
	tmpl        = template.Must(template.New("dirlist").Parse(dirListTemplate))
)

type DirEntry struct {
	URL     string
	Name    string
	Size    string
	ModTime time.Time
	IsDir   bool
	Icon    template.HTML
}

func main() {
	// 初始化参数
	flag.Parse()
	// 初始化 MinIO 客户端
	useSSL := false
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		log.Fatal("MinIO 连接失败: ", err)
	}
	minioClient = client

	http.HandleFunc("/", handler)
	log.Println("服务启动在 " + address + " 端口...")
	log.Fatal(http.ListenAndServe(address, nil))
}

func handler(w http.ResponseWriter, r *http.Request) {
	requestPath := r.URL.Path
	key := strings.TrimPrefix(requestPath, "/")

	// 尝试作为文件处理
	if handleFile(w, key) {
		return
	}

	// 尝试作为目录处理
	if handleDirectory(w, key) {
		return
	}

	// 未找到资源
	http.Error(w, "404 Not Found", http.StatusNotFound)
}

func handleFile(w http.ResponseWriter, key string) bool {
	// 检查文件是否存在
	objInfo, err := minioClient.StatObject(context.Background(), bucket, key, minio.StatObjectOptions{})
	if objInfo.ContentType == "application/x-directory" {
		return false
	}
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return false
		}
		log.Printf("文件检查失败: %v", err)
		return false
	}

	// 获取文件内容
	object, err := minioClient.GetObject(context.Background(), bucket, key, minio.GetObjectOptions{})
	if err != nil {
		log.Printf("文件获取失败: %v", err)
		return false
	}
	defer object.Close()

	// 设置下载头
	w.Header().Set("Content-Type", getContentType(key))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", objInfo.Size))

	// 流式传输内容
	if _, err := io.Copy(w, object); err != nil {
		log.Printf("响应写入失败: %v", err)
	}
	return true
}

func handleDirectory(w http.ResponseWriter, prefix string) bool {
	// 自动添加目录斜杠
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if prefix == "/" {
		prefix = ""
	}

	// 列出目录内容
	ch := minioClient.ListObjects(context.Background(), bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: false,
	})

	var entries []DirEntry
	hasContent := false

	// 添加父目录链接
	if prefix != "" {
		parent := path.Dir(strings.TrimSuffix(prefix, "/")) + "/"
		entries = append(entries, DirEntry{
			URL:     "/" + parent,
			Name:    "..",
			Size:    "-",
			ModTime: time.Time{},
			IsDir:   true,
			Icon:    getFileIcon("dir"),
		})
	}

	// 处理目录结果
	for obj := range ch {
		if obj.Err != nil {
			log.Printf("目录列表错误: %v", obj.Err)
			return false
		}

		hasContent = true

		// 过滤当前目录
		if obj.Key == prefix {
			continue
		}

		if obj.StorageClass == "" {
			// 处理子目录
			entries = append(entries, DirEntry{
				URL:     "/" + obj.Key,
				Name:    path.Base(obj.Key),
				Size:    "-",
				ModTime: time.Time{},
				IsDir:   true,
				Icon:    getFileIcon("dir"),
			})
		} else {
			// 处理文件
			entries = append(entries, DirEntry{
				URL:     "/" + obj.Key,
				Name:    path.Base(obj.Key),
				Size:    formatSize(obj.Size),
				ModTime: obj.LastModified,
				IsDir:   false,
				Icon:    getFileIcon("file"),
			})
		}

	}

	if !hasContent {
		return false
	}

	// 渲染目录列表
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := tmpl.Execute(w, struct {
		Path    string
		Entries []DirEntry
	}{
		Path:    "/" + prefix,
		Entries: entries,
	})

	if err != nil {
		log.Printf("模板渲染失败: %v", err)
	}
	return true
}

func formatSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}

func getContentType(key string) string {
	ext := path.Ext(key)
	switch strings.ToLower(ext) {
	case ".html", ".htm":
		return "text/html"
	case ".css":
		return "text/css"
	case ".js":
		return "application/javascript"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".pdf":
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

// 获取文件类型图标（Base64编码）
func getFileIcon(filename string) template.HTML {
	ext := strings.ToLower(filename)

	// 常见文件类型图标
	switch ext {
	case "dir":
		return `<img src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAABcAAAAWCAYAAAArdgcFAAAAAXNSR0IArs4c6QAAAARnQU1BAACxjwv8YQUAAAAJcEhZcwAADsMAAA7DAcdvqGQAAAH/SURBVEhLxdPfS1phHAZw7/XCP0JBlsIkNhgULAYjUjkzByuDEAx2ZlQWZm4VhZbWoagoibGti7WwGGUUjo3KflLjELHdrLGiHxeDXQi7iA0p4qn37WRl54BIhx54vHrfj3zf9z0KyJgUHggE4HA4JMuyLJLJpLA6s1A8FAohVFsGrsYm2Y7qp2jwepBIJOjGTKLgOA4+J4O96SCw/lqyydUwmpxF6Gj3g+xJbzQaFciLKE6DmR4n/i/3iqLnPeYH8Svigbu0AA7zg2utZ+2IxWICexaKD9U9xs+ROuxPNEp2d/wVPrcX499Sj+ifz4WrYH54D/F4XKAFfHuyDQvddkw0Fkp2qsWMo69hUZhM/e2dCy+td6DVasHz/FVcbFOmJcf1viYPfnsu9Ho9QW8OJ/exPeaVB9/8UIu3L+7Lgx+uDeDHsFsefOejDxFPgTz4wUI3+MEKefDfUy2YbrXIgye+BDHfVXqLOBnv72wnXfwn5qdnuR9toq9ha7SevmdyeeSLvLx24w0rfiwmkwkjwefYjPgw2VyElb5yzHHP8CnwhF7SmPdR6h0TeNidj+9DldfWLvZXoJJJw8mP1WqFy5SD1pK7aCvLzaquU9hoNEKj0UCpVF7gJDabDTqdDgaDIesSWK1Ww2KxUDOFk5AJyEjZVqVSgWEYQUvDbzbACZHvxmyDCBW5AAAAAElFTkSuQmCC" class="icon" alt="[DIR]">`
	default: // file
		return `<img src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAABYAAAAbCAYAAAB4Kn/lAAAAAXNSR0IArs4c6QAAAARnQU1BAACxjwv8YQUAAAAJcEhZcwAADsMAAA7DAcdvqGQAAAIVSURBVEhLtZW/y3FhGMctJilFKZKB8gdYMCmbxUBRJpOYlSxYjQx+LMrERIQQFkQiGaRYlB+hZLKQrqfrzvHynuc8jsf7fusznHPd96f7nHNf9+HAfwoRq9XqX9FsNonku9zFrVYLZrMZK4xGIxSLRQgGg1Cr1Yjo79zFy+WS3GATk8kE4/EYJpMJhMNhqFQqt8qffCTGDAYDSCQSUK1WyTWVj8WYXq8H8Xj86Z3TxO12GzKZDA18p1T0ej3EYjHIZrNQKBQgn8+Dz+cDm80G9XqdjKGJvV4vaDQaGmazmdQxCoUC5HI58Pl8EIvFIBKJQCAQgFQqBZ1OR8bQxIfDAdbrNY3dbkfqGK1WCxKJBIRCIRFS8Hg8ZrHFYgEul0tDpVKROuZyucD5fIZSqQTRaPSO0+lkFm82G5jP5zQWiwWpP+Z0OkGj0SC7AnG5XMziQCAABoOBFdhUx+MRVqsVdDqdn8W4gmQyyYrHp8Bm+VGcTqfJqtkwnU7JHMxLcSQSAYfDwYrhcEjmYF6KsaNwk7Nhv9+TOZiXYrfbDUqlkhWP58NL8WOu1yvZr0xgncpb4lQqRTqMiXK5fBv5pni73UK322UE25/KW2L8O1itVkb6/f5t5Jvi0WgEoVCIEfxNUXlL/E4YxblcjqwA2/Q34IfEM/tJjCc/4vF4wO/3/wq73Q4ymexZTAVvcjicj/hW/O8C8AXGNRjtT4rvuAAAAABJRU5ErkJggg==" class="icon" alt="[FILE]">`
	}
}
