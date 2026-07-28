package handler

import (
	"fmt"
	"strings"

	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"
	"ai-drama-platform/internal/sms"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"
)

// ============================================================
// Admin 批量导入创作者（2026-07-28 会议需求）
// 管理端通过上传 Excel 批量创建创作者账号。
// 对齐财务收益导入的交互模式：模板下载 + 文件上传 + 逐行报告 + dry_run。
// ============================================================

// creatorImportRowReport 创作者导入逐行报告
type creatorImportRowReport struct {
	RowNo     int    `json:"row_no"`
	Phone     string `json:"phone"`
	Name      string `json:"name"`
	OrgName   string `json:"org_name"`
	Status    string `json:"status"` // created / duplicate / failed
	Message    string `json:"message"`
	CreatorID  uint64 `json:"creator_id,omitempty"`
	DuplicateOfRow int `json:"duplicate_of_row,omitempty"`
}

// GET /v1/admin/creators/template.xlsx
// 下载创作者批量导入模板。
func (s *Server) adminDownloadCreatorTemplate(c *gin.Context) {
	xl := excelize.NewFile()
	sheet := "Sheet1"
	headers := []string{"手机号(必填)", "姓名(选填)", "机构名称(选填,机构创作者填写)", "昵称(选填,留空自动生成)"}
	samples := [][]interface{}{
		{"13800138001", "张三", "", ""},
		{"13800138002", "", "杭州某某传媒", "某某传媒官方号"},
	}
	FillSheetHeaders(xl, sheet, headers, samples)
	_ = xl.SetColWidth(sheet, "A", "A", 20)
	_ = xl.SetColWidth(sheet, "B", "B", 20)
	_ = xl.SetColWidth(sheet, "C", "C", 32)
	_ = xl.SetColWidth(sheet, "D", "D", 28)
	WriteXLSXResponse(c, xl, "创作者批量导入模板.xlsx")
}

// POST /v1/admin/creators/import
// 管理端上传 Excel 批量创建创作者。
// 支持 ?dry_run=1 试算（只解析+报告，不写库）。
func (s *Server) adminImportCreators(c *gin.Context) {
	dryRun := c.Query("dry_run") == "1" || c.Query("dry_run") == "true"
	xl, rows, ok := OpenUploadedXLSX(c)
	if !ok {
		return
	}
	defer xl.Close()
	if len(rows) <= 1 {
		response.InvalidParam(c, "表格没有数据行（第 1 行为表头）")
		return
	}

	type parsedCreator struct {
		rowNo   int
		phone   string
		name    string
		orgName string
		nickname string
	}
	var parsed []parsedCreator
	reports := make([]creatorImportRowReport, 0)
	seenPhones := map[string]int{} // phone -> firstRow

	for i := 1; i < len(rows); i++ {
		row := rows[i]
		lineNo := i + 1
		if rowIsBlank(row) {
			continue
		}
		phone := strings.TrimSpace(cellOr(row, 0, ""))
		if phone == "" {
			reports = append(reports, creatorImportRowReport{
				RowNo: lineNo, Status: RowFailed, Message: "手机号不能为空",
			})
			continue
		}
		if !sms.ValidPhone(phone) {
			reports = append(reports, creatorImportRowReport{
				RowNo: lineNo, Phone: phone, Status: RowFailed, Message: "手机号格式不正确",
			})
			continue
		}
		// 文件内去重
		if firstRow, exists := seenPhones[phone]; exists {
			reports = append(reports, creatorImportRowReport{
				RowNo: lineNo, Phone: phone, Status: RowDuplicate,
				Message: fmt.Sprintf("文件内手机号重复，已跳过；首次出现在第%d行", firstRow),
				DuplicateOfRow: firstRow,
			})
			continue
		}
		seenPhones[phone] = lineNo
		pc := parsedCreator{
			rowNo:   lineNo,
			phone:   phone,
			name:    strings.TrimSpace(cellOr(row, 1, "")),
			orgName: strings.TrimSpace(cellOr(row, 2, "")),
			nickname: strings.TrimSpace(cellOr(row, 3, "")),
		}
		parsed = append(parsed, pc)
	}

	batchNo := GenerateBatchNo("CRT")
	createdRows := 0
	duplicateRows := 0
	failedRows := 0

	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, pc := range parsed {
			// 库内查重
			var count int64
			tx.Model(&model.Creator{}).Where("phone = ?", pc.phone).Count(&count)
			if count > 0 {
				reports = append(reports, creatorImportRowReport{
					RowNo: pc.rowNo, Phone: pc.phone, Name: pc.name, OrgName: pc.orgName,
					Status: RowFailed, Message: "手机号已存在",
				})
				failedRows++
				continue
			}

			creator := model.Creator{
				Phone:        pc.phone,
				Name:         pc.name,
				Nickname:     pc.nickname,
				OrgName:      pc.orgName,
				VerifyStatus: model.CreatorVerifyUnverified,
				Status:       model.StatusActive,
			}
			if pc.orgName != "" {
				creator.CreatorType = model.CreatorTypeOrganization
			} else {
				creator.CreatorType = model.CreatorTypePersonal
			}
			applyCreatorDefaults(&creator)

			rp := creatorImportRowReport{
				RowNo: pc.rowNo, Phone: pc.phone, Name: pc.name, OrgName: pc.orgName,
			}

			if !dryRun {
				if err := tx.Create(&creator).Error; err != nil {
					if isUniqueViolation(err) {
						rp.Status = RowFailed
						rp.Message = "手机号已存在（并发冲突）"
						reports = append(reports, rp)
						failedRows++
						continue
					}
					return err
				}
				rp.CreatorID = creator.ID
			}
			rp.Status = RowCreated
			if dryRun {
				rp.Message = "试算：将新增创作者"
			} else {
				rp.Message = "已创建"
			}
			reports = append(reports, rp)
			createdRows++
		}
		return nil
	})
	if err != nil {
		response.ServerError(c, "导入失败，已回滚")
		return
	}

	// 统计 duplicate
	for _, r := range reports {
		if r.Status == RowDuplicate {
			duplicateRows++
		}
	}

	result := gin.H{
		"batch_no":       batchNo,
		"dry_run":        dryRun,
		"processed_rows": len(reports),
		"created_rows":   createdRows,
		"duplicate_rows": duplicateRows,
		"failed_rows":    failedRows,
		"row_reports":    reports,
		"errors":         collectCreatorImportErrors(reports),
	}
	if dryRun {
		result["batch_no"] = ""
		result["note"] = "试算结果（dry_run），未写库。去掉 dry_run 参数即可正式导入。"
	}
	response.OK(c, result)
}

func collectCreatorImportErrors(reports []creatorImportRowReport) []string {
	out := make([]string, 0)
	for _, r := range reports {
		if r.Status == RowFailed || r.Status == RowDuplicate {
			out = append(out, fmt.Sprintf("第%d行：%s", r.RowNo, r.Message))
		}
	}
	return out
}
