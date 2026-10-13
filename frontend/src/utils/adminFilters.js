// 「打印订单」与「余额流水」两个子页共用的筛选项工具。
// 抽出来是因为两个页面各写一份必然会漂（改了一处忘了另一处）。

// 筛选项里的「全部」必须用一个显式哨兵值。
//
// 🚫 绝不能用空字符串：Reka UI 的 SelectItem 明确禁止空串 value
// （那是留给「清空选择、显示 placeholder」的），传进去会在组件更新期
// 抛出 "A <SelectItem /> must have a value prop that is not an empty string"，
// 打崩整棵组件树 —— 表现是「点什么都没反应、一直停在当前页」。
// 实测踩过，控制台里那行错误才是唯一线索。
// scripts/check-frontend.js 的检查 1 盯着这条。
export const FILTER_ALL = 'all'

// 状态/类型 → 中文标签的查表。从同一个 items 数组推导，
// 避免「加了一个类型却忘了加文案」。哨兵值不参与查表。
export function toLabelMap(items) {
  return items.reduce((acc, i) => {
    if (i.value && i.value !== FILTER_ALL) acc[i.value] = i.label
    return acc
  }, {})
}
