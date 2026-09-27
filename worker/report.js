export default {
  async fetch(request, env) {
    if (request.method !== 'POST') return new Response('method not allowed', { status: 405 })
    const report = await request.json()
    if (report.target !== 'issue' && report.target !== 'bot') {
      return new Response('unknown target', { status: 400 })
    }
    if (typeof report.title !== 'string' || typeof report.body !== 'string') {
      return new Response('missing report', { status: 400 })
    }
    try {
      if (report.target === 'issue') await postIssue(env, report)
      else await postBot(env, report)
      return new Response(null, { status: 204 })
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      return new Response(message, { status: 502 })
    }
  },
}

async function postIssue(env, report) {
  const response = await fetch(`https://api.github.com/repos/${env.GITHUB_REPO}/issues`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${env.GITHUB_TOKEN}`,
      accept: 'application/vnd.github+json',
      'user-agent': 'prism-report',
      'content-type': 'application/json',
    },
    body: JSON.stringify({ title: report.title.slice(0, 200), body: report.body }),
  })
  if (!response.ok) throw new Error(`github ${response.status}`)
}

async function postBot(env, report) {
  const text = `${report.title}\n\n${report.body}`.slice(0, 1000)
  const log = typeof report.log === 'string' ? report.log.trim() : ''
  const token = typeof env.TELEGRAM_BOT_TOKEN === 'string' ? env.TELEGRAM_BOT_TOKEN : ''
  const chat = typeof env.TELEGRAM_CHAT_ID === 'string' ? env.TELEGRAM_CHAT_ID : ''
  const form = new FormData()
  form.set('chat_id', chat)
  form.set('caption', text)
  form.set('document', new File([log === '' ? text : log], 'prism-log.txt', { type: 'text/plain' }))
  const file = await fetch(`https://api.telegram.org/bot${token}/sendDocument`, {
    method: 'POST',
    body: form,
  })
  const fileBody = await file.text()
  if (!file.ok) throw new Error(telegramError(file.status, fileBody))
}

function telegramError(status, body) {
  return `telegram ${status} ${body.replace(/bot\d+:[A-Za-z0-9_-]+/g, 'bot').slice(0, 180)}`
}
