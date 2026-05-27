package handler

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

type contractDocData struct {
	ContractNo   string
	CreatorName  string
	CreatorPhone string
	DramaTitle   string
	SignedDate   string
}

func (s *Server) adminDownloadContractTemplate(c *gin.Context) {
	doc, err := buildContractDocx(contractDocData{
		ContractNo:   "【系统生成】",
		CreatorName:  "【创作者姓名】",
		CreatorPhone: "【创作者手机号】",
		DramaTitle:   "【短剧名称】",
		SignedDate:   "【签署日期】",
	})
	if err != nil {
		response.ServerError(c, "生成合同模板失败")
		return
	}
	serveDocx(c, "漫剧合作合同模板.docx", doc)
}

func (s *Server) adminDownloadContractDocx(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	ct, data, err := s.loadContractDocData(id)
	if err != nil {
		if isNotFound(err) {
			response.NotFound(c, "合同不存在")
			return
		}
		response.ServerError(c, "查询合同失败")
		return
	}
	doc, err := buildContractDocx(data)
	if err != nil {
		response.ServerError(c, "生成合同失败")
		return
	}
	serveDocx(c, fmt.Sprintf("%s-漫剧合作合同.docx", ct.ContractNo), doc)
}

func (s *Server) creatorDownloadContractDocx(c *gin.Context) {
	cid := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	ct, data, err := s.loadContractDocData(id)
	if err != nil {
		if isNotFound(err) {
			response.NotFound(c, "合同不存在")
			return
		}
		response.ServerError(c, "查询合同失败")
		return
	}
	if ct.CreatorID != cid {
		response.Forbidden(c, "合同不属于当前创作者")
		return
	}
	doc, err := buildContractDocx(data)
	if err != nil {
		response.ServerError(c, "生成合同失败")
		return
	}
	serveDocx(c, fmt.Sprintf("%s-漫剧合作合同.docx", ct.ContractNo), doc)
}

func (s *Server) loadContractDocData(id uint64) (model.Contract, contractDocData, error) {
	var ct model.Contract
	if err := s.db.First(&ct, id).Error; err != nil {
		return ct, contractDocData{}, err
	}

	var creator model.Creator
	if err := s.db.Select("id", "name", "phone").First(&creator, ct.CreatorID).Error; err != nil {
		return ct, contractDocData{}, err
	}

	dramaTitle := "未绑定具体短剧"
	if ct.DramaID != nil {
		var drama model.Drama
		if err := s.db.Select("id", "title").First(&drama, *ct.DramaID).Error; err == nil {
			dramaTitle = drama.Title
		}
	}

	signedDate := ct.CreatedAt.Format("2006年01月02日")
	if ct.CreatedAt.IsZero() {
		signedDate = time.Now().Format("2006年01月02日")
	}
	return ct, contractDocData{
		ContractNo:   ct.ContractNo,
		CreatorName:  creator.Name,
		CreatorPhone: creator.Phone,
		DramaTitle:   dramaTitle,
		SignedDate:   signedDate,
	}, nil
}

func serveDocx(c *gin.Context, filename string, content []byte) {
	escaped := url.QueryEscape(filename)
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"contract.docx\"; filename*=UTF-8''%s", escaped))
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", content)
}

func buildContractDocx(data contractDocData) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	files := map[string]string{
		"[Content_Types].xml": contentTypesXML,
		"_rels/.rels":         packageRelsXML,
		"word/document.xml":   contractDocumentXML(data),
	}
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func contractDocumentXML(data contractDocData) string {
	lines := []string{
		"漫剧合作合同",
		"",
		"合同编号：" + emptyAsPlaceholder(data.ContractNo, "【系统生成】"),
		"甲方：北京共绩科技有限公司",
		"乙方：" + emptyAsPlaceholder(data.CreatorName, "【创作者姓名】"),
		"乙方联系方式：" + emptyAsPlaceholder(data.CreatorPhone, "【创作者手机号】"),
		"合作短剧：" + emptyAsPlaceholder(data.DramaTitle, "【短剧名称】"),
		"",
		"一、合作内容",
		"1. 甲方平台为乙方创作或授权的漫剧 / 短剧内容提供发布、推广、数据统计和收益结算服务。",
		"2. 乙方确认其对合作内容拥有合法、完整、可授权的权利，并保证内容不侵犯任何第三方合法权益。",
		"",
		"二、收益结算",
		"1. 合作内容产生的平台收益、第三方渠道收益，以后台系统记录及双方确认的财务数据为准。",
		"2. 第三方渠道收益可由甲方通过 Excel 导入，字段为：短剧名称、渠道、收益、日期。",
		"3. 乙方收益按双方约定比例结算，具体比例以平台后台配置或双方补充协议为准。",
		"",
		"三、数据与对账",
		"1. 甲方应保留订单、播放、渠道收益及提现记录，供双方核验。",
		"2. 如乙方对收益数据有异议，应在收到数据后 7 个工作日内提出，双方协商确认。",
		"",
		"四、提现与付款",
		"1. 乙方达到最低提现门槛后，可在创作者端发起提现申请。",
		"2. 甲方审核通过后，按乙方提交的收款账户进行付款。",
		"",
		"五、其他",
		"1. 本合同为 MVP 阶段系统生成模板，正式签署前可根据双方实际商务条款补充调整。",
		"2. 本合同自双方确认或签署之日起生效。",
		"",
		"甲方（盖章）：北京共绩科技有限公司",
		"乙方（签字）：",
		"签署日期：" + emptyAsPlaceholder(data.SignedDate, "【签署日期】"),
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for i, line := range lines {
		if i == 0 {
			b.WriteString(`<w:p><w:pPr><w:jc w:val="center"/></w:pPr><w:r><w:rPr><w:b/><w:sz w:val="32"/></w:rPr><w:t>`)
			b.WriteString(xmlEscape(line))
			b.WriteString(`</w:t></w:r></w:p>`)
			continue
		}
		b.WriteString(`<w:p><w:r><w:t xml:space="preserve">`)
		b.WriteString(xmlEscape(line))
		b.WriteString(`</w:t></w:r></w:p>`)
	}
	b.WriteString(`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/></w:sectPr>`)
	b.WriteString(`</w:body></w:document>`)
	return b.String()
}

func emptyAsPlaceholder(value, placeholder string) string {
	if strings.TrimSpace(value) == "" {
		return placeholder
	}
	return value
}

func xmlEscape(value string) string {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(value)); err != nil {
		return value
	}
	return buf.String()
}

const contentTypesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`

const packageRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`
