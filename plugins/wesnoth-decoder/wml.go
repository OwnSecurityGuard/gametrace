package main

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"strings"
)

// maxDecompressed 是单个 WML payload 解压后的上限（对齐服务端 document_size_limit
// 40MB 的量级，防止畸形/超大包耗尽内存）。
const maxDecompressed = 40 << 20

// wmlNode 是 simple_wml 文本解析出的一个节点。文档根节点 name 为空字符串。
type wmlNode struct {
	name     string
	attrs    []wmlAttr // 属性，保持线上顺序
	children []*wmlNode
}

// wmlAttr 是单个属性。WML 属性值一律是字符串（类型化在游戏层完成）。
type wmlAttr struct {
	key   string
	value string
}

// decompressWML 解压一帧 payload 得到 WML 文本字节。
//
// 线上格式（src/server/common/simple_wml.cpp compress_buffer/uncompress_buffer）：
//   - 首字节 'B'（0x42，即 bzip2 的 "BZh" 魔数头）→ bzip2 流；
//   - 其余 → gzip 流（gzip 魔数 0x1f 0x8b）。
//
// 解压失败返回 ok=false，由调用方产出兜底事件，绝不中断解码流。
func decompressWML(body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return nil, false
	}
	var r io.Reader
	if body[0] == 'B' {
		r = bzip2.NewReader(bytes.NewReader(body))
	} else {
		gr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, false
		}
		defer gr.Close()
		r = gr
	}
	out, err := io.ReadAll(io.LimitReader(r, maxDecompressed))
	if err != nil {
		return nil, false
	}
	return out, true
}

// parseWML 解析 simple_wml 文本，返回根节点。
//
// 语法（对齐 src/server/common/simple_wml.cpp 的 node 文本解析）：
//   - 根节点无 tag；子节点为 [tag] ... [/tag]，可嵌套递归；
//   - 属性为 key=value 或 key="value"，每行一条；引号值支持 "" 转义与
//     +" 续行（跨行拼接，续行段可带 # 注释 / 缩进 / _ 翻译标记）；
//   - '#' 开头为注释直到行尾；
//   - 空白（空格/制表/换行）在 token 间自由跳过。
//
// 解析宽容：遇到未闭合引号、缺失 '='、tag 不匹配等异常一律跳过而非报错，
// 保证单个坏包不会拖垮同流后续消息。仅当文本结构性损坏（如未闭合的 '['）
// 才返回 error，调用方据此产出 unknown 兜底事件。
func parseWML(text string) (*wmlNode, error) {
	root := &wmlNode{}
	stack := []*wmlNode{root}
	n := len(text)
	p := 0

	for p < n {
		c := text[p]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			p++
		case c == '#': // 注释到行尾
			for p < n && text[p] != '\n' {
				p++
			}
		case c == '[':
			end := strings.IndexByte(text[p:], ']')
			if end < 0 {
				return nil, fmt.Errorf("unterminated '[' at byte %d", p)
			}
			name := text[p+1 : p+end]
			p += end + 1
			if strings.HasPrefix(name, "/") {
				// 闭 tag：精确匹配栈顶；不匹配则向上找同名祖先（宽容），
				// 找不到就忽略（容忍缺闭的坏包）。
				name = strings.TrimPrefix(name, "/")
				for i := len(stack) - 1; i > 0; i-- {
					if stack[i].name == name {
						stack = stack[:i]
						break
					}
				}
				continue
			}
			cur := stack[len(stack)-1]
			child := &wmlNode{name: name}
			cur.children = append(cur.children, child)
			stack = append(stack, child)
		default:
			// 属性：key=value（key 内不含 '='，value 可以含）。
			// '=' 的搜索限在本行内，避免把跨行的垃圾文本吞进 key。
			lineEnd := strings.IndexByte(text[p:], '\n')
			searchEnd := n
			if lineEnd >= 0 {
				searchEnd = p + lineEnd
			}
			eq := strings.IndexByte(text[p:searchEnd], '=')
			if eq < 0 {
				// 本行既不是 tag 也不是属性：跳过本行。
				p = searchEnd + 1
				continue
			}
			key := strings.TrimSpace(text[p : p+eq])
			p += eq + 1
			value, next, ok := readAttrValue(text, p)
			if ok && key != "" {
				cur := stack[len(stack)-1]
				cur.attrs = append(cur.attrs, wmlAttr{key: key, value: value})
			}
			p = next
		}
	}
	return root, nil
}

// readAttrValue 读取属性值，返回 (value, 下一字节位置, ok)。
//
// 三种形态（语义对齐 simple_wml.cpp）：
//   - "..."：引号值，支持 "" 转义为单个引号、+" 续行拼接多段；
//   - _"..." / _ "..."：gettext 翻译标记，跳过 '_' 后按引号值读；
//   - 无引号：读到行尾（含 '='，末尾空白裁掉）。
//
// 异常形态（未闭合引号、续行格式不对）宽容收尾并返回已读到的内容。
func readAttrValue(text string, p int) (string, int, bool) {
	n := len(text)
	for p < n && (text[p] == ' ' || text[p] == '\t') {
		p++
	}
	if p >= n {
		return "", p, false
	}
	if text[p] == '_' { // 翻译标记：_ "..." 或 _"..."
		p++
		for p < n && (text[p] == ' ' || text[p] == '\t') {
			p++
		}
	}
	if p >= n {
		return "", p, false
	}
	if text[p] != '"' {
		// 无引号值：到行尾。
		e := p
		for e < n && text[e] != '\n' {
			e++
		}
		return strings.TrimRight(text[p:e], " \t"), e, true
	}

	// 引号值："" 转义 + +" 续行。
	var sb strings.Builder
	p++ // 跳过开引号
	for {
		i := strings.IndexByte(text[p:], '"')
		if i < 0 {
			return sb.String(), n, false // 未闭合
		}
		q := p + i
		if q+1 < n && text[q+1] == '"' { // 转义的引号 ""
			sb.WriteString(text[p : q+1])
			p = q + 2
			continue
		}
		sb.WriteString(text[p:q])
		p = q + 1

		// 闭引号后：可选空白 + 换行结束，或 + 换行续行。
		j := p
		for j < n && (text[j] == ' ' || text[j] == '\t') {
			j++
		}
		if j >= n || text[j] == '\n' {
			return sb.String(), j + 1, true
		}
		if text[j] != '+' {
			return sb.String(), j, true // 无续行标记，宽容结束
		}
		j++
		for j < n && text[j] == ' ' {
			j++
		}
		if j >= n || text[j] != '\n' {
			return sb.String(), j, true
		}
		// 续行段前奏：跳过 # 注释、缩进、_ 翻译标记，然后必须是 '"'。
		p = j + 1
		for p < n {
			switch text[p] {
			case '#': // 注释行整体跳过（含换行）
				for p < n && text[p] != '\n' {
					p++
				}
				if p < n {
					p++ // 消费 '\n'
				}
			case ' ', '\t', '\r', '_':
				p++
			default:
				goto preludeDone
			}
		}
	preludeDone:
		if p >= n || text[p] != '"' {
			return sb.String(), p, true // 续行格式异常，宽容结束
		}
		// simple_wml 语义：+" 续行在拼接处插入一个换行。
		sb.WriteByte('\n')
		p++ // 进入下一段引号内容
	}
}
