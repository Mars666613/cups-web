// 打印参数的选项列表 —— 打印表单与管理端价目表【共用同一份】。
//
// 单独抽出来是因为计价维度必须与用户实际能选的值完全对齐：
// 管理端若按自己的列表配了 "A2"，而打印表单的纸张尺寸里没有 A2，
// 那条价目就是永远不会被命中的死配置，而且没有任何地方会报错。
//
// 这些值与后端 ipp.PrintJobOptions 的 PaperSize / PaperType 取值一一对应。

export const paperSizeItems = [
  { label: 'A5 (148×210mm)', value: 'A5' },
  { label: 'A4 (210×297mm)', value: 'A4' },
  { label: 'A3 (297×420mm)', value: 'A3' },
  { label: 'A2 (420×594mm)', value: 'A2' },
  { label: 'A1 (594×841mm)', value: 'A1' },
  { label: '5寸 (89×127mm)', value: '5inch' },
  { label: '6寸 (102×152mm)', value: '6inch' },
  { label: '7寸 (127×178mm)', value: '7inch' },
  { label: '8寸 (152×203mm)', value: '8inch' },
  { label: '10寸 (203×254mm)', value: '10inch' },
  { label: 'Letter (8.5×11in)', value: 'Letter' },
  { label: 'Legal (8.5×14in)', value: 'Legal' }
]

export const paperTypeItems = [
  { label: '普通纸', value: 'plain' },
  { label: '照片纸', value: 'photo' },
  { label: '光面照片纸', value: 'glossy' },
  { label: '哑光照片纸', value: 'matte' },
  { label: '信封', value: 'envelope' },
  { label: '卡片纸', value: 'cardstock' },
  { label: '标签纸', value: 'labels' },
  { label: '自动选择', value: 'auto' }
]
