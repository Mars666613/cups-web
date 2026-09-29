export function isOfficeFile(f) {
  return /\.(docx?|pptx?|xlsx?)$/i.test(f.name) || [
    'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
    'application/msword',
    'application/vnd.openxmlformats-officedocument.presentationml.presentation',
    'application/vnd.ms-powerpoint',
    'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
    'application/vnd.ms-excel'
  ].includes(f.type)
}

export function isOFDFile(f) {
  return /\.ofd$/i.test(f.name)
}
