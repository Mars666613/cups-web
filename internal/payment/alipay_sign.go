package payment

// 支付宝 RSA2 签名与验签的原语。
//
// 单独一个文件是因为这里的每一条规则都能让验签【静默】失败 —— 报错信息
// 通常只有一句「验签失败」，根本定位不到是哪条规则没遵守。把规则连同
// 理由集中写在一处，比散在业务流程里强。
//
// 不引第三方 SDK 的理由见 alipay.go 顶部。

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// alipaySignContent 把参数拼成待签名串。
//
// 规则：
//  1. 按参数名 ASCII 升序排列
//  2. 排除 sign 本身
//  3. 跳过值为空的参数
//  4. includeSignType 决定 sign_type 算不算在内（见下）
//
// 值【不做】URL 编码：签名针对的是解码后的原文，编码后的串是另一回事。
//
// ⚠️ includeSignType 这一条【不能】统一，请求与通知的规则是相反的：
//
//	【请求签名】includeSignType = true
//	   网关把 sign_type 也算进验签串。这一条实测出来的 —— 少了它，
//	   支付宝回 40002 / isv.invalid-signature，并把它用的字符串回显给你，
//	   一眼就能看出只差了 &sign_type=RSA2 这一段。
//
//	【异步通知验签】includeSignType = false
//	   官方 SDK 的 rsaCheckV1 会同时移除 sign 与 sign_type 再验签。
//
// 搞反任何一个，表现都是同一句「验签失败」。而且通知侧没有回显，
// 只能靠这条规则 —— 所以两边都用同一个参数的调用点必须写清楚。
func alipaySignContent(params url.Values, includeSignType bool) string {
	keys := make([]string, 0, len(params))
	for k, vs := range params {
		if k == "sign" {
			continue
		}
		if k == "sign_type" && !includeSignType {
			continue
		}
		if len(vs) == 0 || vs[0] == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(params.Get(k))
	}
	return b.String()
}

// alipaySign 用应用私钥签名，返回 base64 的签名值。
func alipaySign(content string, key *rsa.PrivateKey) (string, error) {
	sum := sha256.Sum256([]byte(content))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("签名失败: %w", err)
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// alipayVerify 用支付宝公钥验签。失败一律包成 ErrBadSignature。
func alipayVerify(content, sign string, key *rsa.PublicKey) error {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sign))
	if err != nil {
		return fmt.Errorf("%w: 签名不是合法 base64", ErrBadSignature)
	}
	sum := sha256.Sum256([]byte(content))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	return nil
}

// alipayKeyDER 把用户粘贴的密钥还原成 DER。
//
// 开放平台上复制出来的东西形态很杂：可能是 PEM（带 -----BEGIN----- 头）、
// 可能是光秃秃的一行 base64、可能中间被编辑器插入换行。三种都认，
// 统一剥成 DER 再交给 x509 解析。
func alipayKeyDER(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("密钥为空")
	}
	if strings.Contains(s, "-----BEGIN") {
		block, _ := pem.Decode([]byte(s))
		if block == nil {
			return nil, errors.New("PEM 格式无法解析")
		}
		return block.Bytes, nil
	}
	// 裸 base64：去掉所有空白字符再解码。
	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r':
			return -1
		}
		return r
	}, s)
	der, err := base64.StdEncoding.DecodeString(compact)
	if err != nil {
		return nil, fmt.Errorf("密钥既不是 PEM 也不是合法 base64: %w", err)
	}
	return der, nil
}

// parseAlipayPrivateKey 解析应用私钥，PKCS8 与 PKCS1 都支持。
//
// 🚫 不做「猜」以外的任何补救：两种都解析不出来就报错，绝不静默用别的密钥。
func parseAlipayPrivateKey(s string) (*rsa.PrivateKey, error) {
	der, err := alipayKeyDER(s)
	if err != nil {
		return nil, fmt.Errorf("应用私钥: %w", err)
	}
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("应用私钥不是 RSA 密钥（支付宝只支持 RSA2）")
		}
		return rk, nil
	}
	rk, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("应用私钥既不是 PKCS8 也不是 PKCS1: %w", err)
	}
	return rk, nil
}

// parseAlipayPublicKey 解析支付宝公钥。
//
// ⚠️ 这里要的是【支付宝公钥】，不是应用公钥 —— 开放平台上这两个长得很像，
// 传错的话所有验签都会失败，而报错只有一句「验签失败」。文档与配置项
// 都写明这一点，就是为了少一次这种排查。
func parseAlipayPublicKey(s string) (*rsa.PublicKey, error) {
	der, err := alipayKeyDER(s)
	if err != nil {
		return nil, fmt.Errorf("支付宝公钥: %w", err)
	}
	if k, err := x509.ParsePKIXPublicKey(der); err == nil {
		rk, ok := k.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("支付宝公钥不是 RSA 密钥")
		}
		return rk, nil
	}
	rk, err := x509.ParsePKCS1PublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("支付宝公钥既不是 PKIX 也不是 PKCS1: %w", err)
	}
	return rk, nil
}

// centsToYuan 把「分」转成渠道要求的「元」字符串（固定两位小数）。
//
// 🚫 不要用 float64：金额经过浮点数就可能出现「0.1+0.2=0.30000000000000004」
// 这类表示误差，在支付场景里是真实的金额差错。整数除法与取余能精确表达。
func centsToYuan(cents int64) string {
	neg := cents < 0
	if neg {
		cents = -cents
	}
	s := strconv.FormatInt(cents/100, 10) + "." + fmt.Sprintf("%02d", cents%100)
	if neg {
		return "-" + s
	}
	return s
}

// yuanToCents 把渠道返回的「元」字符串转回「分」。
//
// 同样是定点解析而非 ParseFloat。多出的位数只接受【全零】（"0.800" 可以，
// "0.805" 直接报错）—— 金额上宁可失败也不要静默丢精度。
func yuanToCents(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("金额为空")
	}
	neg := false
	if strings.HasPrefix(s, "-") {
		neg, s = true, s[1:]
	}
	intPart, fracPart, hasDot := strings.Cut(s, ".")
	if !hasDot {
		fracPart = ""
	}
	if intPart == "" {
		intPart = "0"
	}
	if intPart == "" || !isAllDigits(intPart) || !isAllDigits(fracPart) {
		return 0, fmt.Errorf("金额不是合法数字: %q", s)
	}
	if len(fracPart) > 2 {
		for _, c := range fracPart[2:] {
			if c != '0' {
				return 0, fmt.Errorf("金额精度超过分: %q", s)
			}
		}
		fracPart = fracPart[:2]
	}
	units, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("金额整数部分溢出: %q", s)
	}
	frac := int64(0)
	if fracPart != "" {
		padded := (fracPart + "00")[:2]
		frac, err = strconv.ParseInt(padded, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("金额小数部分非法: %q", s)
		}
	}
	total := units*100 + frac
	if neg {
		total = -total
	}
	return total, nil
}

func isAllDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// rawResponseNode 从原始响应体里切出 key 对应的【原始 JSON 字节】。
//
// 🚫 这是接支付宝最容易踩的坑：sign 是对响应节点【原文】的签名。
// 若把响应 Unmarshal 成 map 再 Marshal 回去，键序、空格、转义都可能变，
// 得到的字节和签名时的不一样，验签必然失败 —— 而且失败得毫无线索。
// 所以只能按字节从原始 body 里切。
func rawResponseNode(raw []byte, key string) ([]byte, error) {
	needle := []byte(`"` + key + `"`)
	i := bytes.Index(raw, needle)
	if i < 0 {
		return nil, fmt.Errorf("响应里没有 %s 节点", key)
	}
	j := i + len(needle)
	for j < len(raw) && (raw[j] == ':' || raw[j] == ' ' || raw[j] == '\t' || raw[j] == '\n' || raw[j] == '\r') {
		j++
	}
	if j >= len(raw) {
		return nil, fmt.Errorf("响应里 %s 节点没有值", key)
	}
	switch raw[j] {
	case '{', '[':
		end, err := matchJSONValue(raw, j)
		if err != nil {
			return nil, err
		}
		return raw[j:end], nil
	default:
		// 标量值：读到逗号或右花括号为止。
		end := j
		for end < len(raw) && raw[end] != ',' && raw[end] != '}' && raw[end] != ']' {
			end++
		}
		return bytes.TrimSpace(raw[j:end]), nil
	}
}

// matchJSONValue 从 start（一个 { 或 [）开始，返回配平后的结束下标（不含）。
//
// 只数【同一种】括号是对的：JSON 里 { } 与 [ ] 各自配对，
// 嵌套的另一种括号必然自身配平，不会干扰计数。
// 字符串内部要跳过，否则值里的 "}" 会提前把计数打平。
func matchJSONValue(raw []byte, start int) (int, error) {
	var open, close_ byte = '{', '}'
	if raw[start] == '[' {
		open, close_ = '[', ']'
	}
	depth := 0
	inStr := false
	escaped := false

	for i := start; i < len(raw); i++ {
		c := raw[i]
		if inStr {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case open:
			depth++
		case close_:
			depth--
			if depth == 0 {
				return i + 1, nil
			}
		}
	}
	return 0, errors.New("JSON 值不闭合")
}
