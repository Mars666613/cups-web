package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cups-web/internal/store"
)

// TestAdminUpdatePricesPartialUpdate 钉住 PUT /api/admin/prices 的
// 「省略即不改」语义 —— 管理端把「开启计费」开关从表单里独立出来
// （改动即提交，不再点保存）完全依赖它。
//
// 若这三个字段的指针类型被改成值类型（或 handler 改成无条件写入），
// 那么单独切一次开关就会把整张价目表与兜底单价一起清空：
// 管理员只想临时停收/恢复收费，代价却是价目全丢。这个测试就是来喊停的。
func TestAdminUpdatePricesPartialUpdate(t *testing.T) {
	base := pricesSeed{
		enabled:  false,
		fallback: 30,
		rules: []store.PriceRule{
			{IsColor: false, PaperSize: "A4", PaperType: "plain", UnitPriceCents: 20},
		},
	}
	empty := pricesSeed{} // 没有价目、兜底为 0

	cases := []struct {
		name string
		seed pricesSeed
		body string
		code int
		want pricesSeed
	}{
		{
			// 开关独立提交的主路径。
			name: "只切开关：价目与兜底单价原样保留",
			seed: base,
			body: `{"enabled":true}`,
			code: http.StatusOK,
			want: pricesSeed{enabled: true, fallback: 30, rules: base.rules},
		},
		{
			name: "只改价目：开关与兜底单价原样保留",
			seed: base,
			body: `{"rules":[{"isColor":true,"paperSize":"A4","paperType":"plain","unitCents":100}]}`,
			code: http.StatusOK,
			want: pricesSeed{
				enabled:  false,
				fallback: 30,
				rules: []store.PriceRule{
					{IsColor: true, PaperSize: "A4", PaperType: "plain", UnitPriceCents: 100},
				},
			},
		},
		{
			name: "只改兜底单价：开关与价目原样保留",
			seed: base,
			body: `{"fallbackUnitCents":0}`,
			code: http.StatusOK,
			want: pricesSeed{enabled: false, fallback: 0, rules: base.rules},
		},
		{
			// 空对象 = 什么都不改。前端不该发这种请求，但发来了也不能有副作用。
			name: "空对象：什么都不动",
			seed: base,
			body: `{}`,
			code: http.StatusOK,
			want: base,
		},
		{
			// 独立开关不等于绕过校验：没价目也没兜底时拨开开关仍要被拒，
			// 否则用户那边所有打印都算成 0 元，而管理员以为自己已经在收费了。
			name: "只切开关但没配价目：拒绝，且不落库",
			seed: empty,
			body: `{"enabled":true}`,
			code: http.StatusBadRequest,
			want: empty,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			restore := setTestStoreForProviders(t)
			defer restore()

			seedPrices(t, c.seed)

			req := httptest.NewRequest(http.MethodPut, "/api/admin/prices", strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			adminUpdatePricesHandler(rec, req)

			if rec.Code != c.code {
				t.Fatalf("HTTP %d，期望 %d（响应体：%s）", rec.Code, c.code, strings.TrimSpace(rec.Body.String()))
			}

			got := readPrices(t)
			if got.enabled != c.want.enabled {
				t.Errorf("enabled = %v，期望 %v", got.enabled, c.want.enabled)
			}
			if got.fallback != c.want.fallback {
				t.Errorf("兜底单价 = %d 分，期望 %d 分（被本次请求意外改动了）", got.fallback, c.want.fallback)
			}
			if len(got.rules) != len(c.want.rules) {
				t.Fatalf("价目条数 = %d，期望 %d（被本次请求意外改动了）", len(got.rules), len(c.want.rules))
			}
			for i := range got.rules {
				// 只比业务维度：ID 与 UpdatedAt 是数据库回填的，
				// 期望值里给不出也没必要给。
				if ruleDim(got.rules[i]) != ruleDim(c.want.rules[i]) {
					t.Errorf("价目[%d] = %+v，期望 %+v", i, got.rules[i], c.want.rules[i])
				}
			}
		})
	}
}

// ruleDim 抽出一条价目的业务维度（不含 ID / UpdatedAt）。
func ruleDim(r store.PriceRule) [4]interface{} {
	return [4]interface{}{r.IsColor, r.PaperSize, r.PaperType, r.UnitPriceCents}
}

type pricesSeed struct {
	enabled  bool
	fallback int64
	rules    []store.PriceRule
}

func seedPrices(t *testing.T, s pricesSeed) {
	t.Helper()
	ctx := context.Background()
	err := appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
		if err := store.SetSettingInt(ctx, tx, store.SettingBillingEnabled, boolToInt(s.enabled)); err != nil {
			return err
		}
		if err := store.SetSettingInt(ctx, tx, store.SettingPriceFallback, s.fallback); err != nil {
			return err
		}
		return store.ReplacePriceRules(ctx, tx, s.rules)
	})
	if err != nil {
		t.Fatalf("准备价格配置失败: %v", err)
	}
}

func readPrices(t *testing.T) pricesSeed {
	t.Helper()
	ctx := context.Background()
	var out pricesSeed
	err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		v, err := store.GetSettingInt(ctx, tx, store.SettingBillingEnabled, 0)
		if err != nil {
			return err
		}
		out.enabled = v != 0
		if out.fallback, err = store.GetSettingInt(ctx, tx, store.SettingPriceFallback, 0); err != nil {
			return err
		}
		out.rules, err = store.ListPriceRules(ctx, tx)
		return err
	})
	if err != nil {
		t.Fatalf("读回价格配置失败: %v", err)
	}
	return out
}
