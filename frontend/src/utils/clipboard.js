// 剪贴板复制工具。

function fallbackCopy(text, done) {
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.setAttribute("readonly", "");
  ta.style.position = "fixed";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch {
    ok = false;
  }
  document.body.removeChild(ta);
  if (ok) done();
  else window.alert("复制失败，请手动复制：" + text);
}

// 复制文本，完成后调用 done。
export function copyText(text, done) {
  const finish = () => {
    if (done) done();
  };
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(finish, () => fallbackCopy(text, finish));
  } else {
    fallbackCopy(text, finish);
  }
}
