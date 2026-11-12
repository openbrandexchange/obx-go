// obx-checker: the online compatibility check of openbrandexchange.com.
//
// The website's Pages Function /api/check calls this Worker over a service
// binding (the Worker has no public route and workers.dev is off). The Worker
//   1. verifies the Cloudflare Turnstile token,
//   2. asks the Limiter (a Durable Object in the EU jurisdiction) for a slot:
//      5 checks per visitor per hour, one check per portal at a time with a
//      10-minute cooldown, a daily cap per portal and in total, at most
//      MAX_RUNNING checks at the same time,
//   3. runs `obx-conformance check-server` in a container pinned to the EU,
//   4. streams the results (NDJSON) back and stores only the cleaned report
//      under a random id in an R2 bucket in the EU jurisdiction (30 days).
//
// Logs carry metadata only (portal host, duration, counts). The client
// secret only passes through memory: it is never logged or stored, and it
// is removed from the results once more before they leave the Worker.

import { Container } from "@cloudflare/containers";
import { DurableObject } from "cloudflare:workers";

export class CheckerContainer extends Container {
	defaultPort = 8080;
	sleepAfter = "5m";
	enableInternet = true;
}

const HOUR = 3600_000;
const DAY = 24 * HOUR;
const REPORT_DAYS = 30;

const json = (status, body, headers = {}) =>
	new Response(JSON.stringify(body), {
		status,
		headers: { "Content-Type": "application/json", "Cache-Control": "no-store", ...headers },
	});

const fail = (status, code, message, retryAfter) =>
	json(status, { error: { code, message, ...(retryAfter ? { retry_after: Math.ceil(retryAfter) } : {}) } });

export default {
	async fetch(request, env, ctx) {
		const url = new URL(request.url);
		try {
			if (url.pathname === "/check" && request.method === "POST") return await startCheck(request, env, ctx);
			if (url.pathname === "/check" && request.method === "GET") return await getReport(url, env);
			return fail(404, "not_found", "not found");
		} catch (err) {
			console.log(JSON.stringify({ event: "error", message: String(err && err.message) }));
			return fail(503, "unavailable", "The checker is not available right now; try again later.");
		}
	},
};

async function startCheck(request, env, ctx) {
	let body;
	try {
		body = await request.json();
	} catch {
		return fail(400, "bad_request", "The request body is not valid JSON.");
	}
	const target = String(body.url || "").trim();
	const clientId = String(body.client_id || "").trim();
	const clientSecret = String(body.client_secret || "").trim();
	if (!target || target.length > 2048) return fail(400, "invalid_url", "Enter the address of the portal.");
	if ((clientId && !clientSecret) || (!clientId && clientSecret))
		return fail(400, "bad_client", "Give both the client id and the client secret of the test API key.");

	const ip = request.headers.get("X-OBX-Client-IP") || "unknown";
	const operator = env.OPERATOR_TOKEN && request.headers.get("X-OBX-Operator") === env.OPERATOR_TOKEN;
	if (!operator && !(await turnstileOK(env, String(body.turnstile || ""), ip))) {
		return fail(403, "turnstile", "The anti-bot check failed; reload the page and try again.");
	}

	let host;
	try {
		host = new URL(target.includes("://") ? target : "https://" + target).hostname.toLowerCase();
	} catch {
		return fail(400, "invalid_url", "This is not a valid web address.");
	}

	const limiters = env.LIMITER.jurisdiction("eu");
	const limiter = limiters.get(limiters.idFromName("global"));
	const ipHash = await hmac(env.IP_SALT || "obx", ip);
	const slot = await limiter.begin(ipHash, host, host === env.DEMO_HOST);
	if (!slot.ok) return fail(429, slot.code, slot.message, slot.retryAfter);

	const started = Date.now();
	let upstream;
	try {
		const containers = env.CHECKER.jurisdiction("eu");
		const container = containers.get(containers.idFromName("checker-" + slot.slot));
		upstream = await container.fetch("http://checker/check", {
			method: "POST",
			headers: { "Content-Type": "application/json", Accept: "application/x-ndjson" },
			body: JSON.stringify({ url: target, ...(clientId ? { client: { id: clientId, secret: clientSecret } } : {}) }),
		});
	} catch (err) {
		await limiter.end(slot.runId, false);
		throw err;
	}
	if (!upstream.ok || !(upstream.headers.get("Content-Type") || "").includes("ndjson")) {
		// Refused before the run (address checks): nothing ran, so no cooldown.
		await limiter.end(slot.runId, false);
		const text = await upstream.text();
		let err;
		try {
			err = JSON.parse(text).error;
		} catch {}
		console.log(JSON.stringify({ event: "refused", host, code: err ? err.code : upstream.status }));
		if (err && err.code) return json(upstream.status === 503 ? 503 : 400, { error: err });
		return fail(503, "unavailable", "The checker is not available right now; try again later.");
	}

	const clean = cleaner(clientId, clientSecret);
	const { readable, writable } = new TransformStream();
	const writer = writable.getWriter();
	const enc = new TextEncoder();
	let open = true;
	const send = async (obj) => {
		if (!open) return;
		try {
			await writer.write(enc.encode(JSON.stringify(obj) + "\n"));
		} catch {
			open = false; // the visitor left; the run continues and its report is stored
		}
	};

	ctx.waitUntil(
		(async () => {
			let report = null;
			let results = 0;
			try {
				const reader = upstream.body.pipeThrough(new TextDecoderStream()).getReader();
				let buf = "";
				for (;;) {
					const { value, done } = await reader.read();
					if (value) buf += value;
					let nl;
					while ((nl = buf.indexOf("\n")) >= 0) {
						const line = buf.slice(0, nl).trim();
						buf = buf.slice(nl + 1);
						if (!line) continue;
						let msg;
						try {
							msg = JSON.parse(clean(line));
						} catch {
							continue;
						}
						if (msg.type === "result") {
							results++;
							await send({ type: "result", result: msg.result });
						} else if (msg.type === "report") {
							report = msg.report;
						} else if (msg.type === "error") {
							await send({ type: "error", error: msg.error });
						}
					}
					if (done) break;
				}
				if (report) {
					const id = randomId();
					const created = new Date();
					await env.REPORTS.put(`reports/${id}.json`, JSON.stringify(report), {
						httpMetadata: { contentType: "application/json" },
						customMetadata: { created: created.toISOString() },
					});
					await send({ type: "report", id, report });
				} else {
					await send({ type: "error", error: { code: "internal", message: "The check stopped before it finished." } });
				}
			} catch (err) {
				await send({ type: "error", error: { code: "internal", message: "The check stopped before it finished." } });
			} finally {
				await limiter.end(slot.runId, true);
				if (open) await writer.close().catch(() => {});
				console.log(
					JSON.stringify({
						event: "check",
						host,
						key: Boolean(clientId),
						duration_ms: Date.now() - started,
						results,
						passed: report ? report.passed : null,
						failed: report ? report.failed : null,
						skipped: report ? report.skipped : null,
						requests: report ? report.requests : null,
						setup_error: report ? Boolean(report.setup_error) : null,
					}),
				);
			}
		})(),
	);

	return new Response(readable, {
		headers: { "Content-Type": "application/x-ndjson", "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" },
	});
}

async function getReport(url, env) {
	const id = url.searchParams.get("id") || "";
	if (!/^[A-Za-z0-9_-]{22}$/.test(id)) return fail(404, "not_found", "This result does not exist or has expired.");
	const obj = await env.REPORTS.get(`reports/${id}.json`);
	if (!obj) return fail(404, "not_found", "This result does not exist or has expired.");
	const created = new Date(obj.customMetadata?.created || obj.uploaded);
	const expires = new Date(created.getTime() + REPORT_DAYS * DAY);
	if (Date.now() > expires.getTime()) return fail(404, "not_found", "This result does not exist or has expired.");
	const report = await obj.json();
	return json(200, { id, created_at: created.toISOString(), expires_at: expires.toISOString(), report }, {
		"Cache-Control": "public, max-age=300",
	});
}

// cleaner removes the visitor's client id and secret from a line once more
// (the check already redacts secrets and tokens).
function cleaner(clientId, clientSecret) {
	const parts = [];
	if (clientSecret.length >= 4) parts.push([clientSecret, "[redacted]"]);
	if (clientId.length >= 4) parts.push([clientId, "[client id]"]);
	for (const [s] of parts.slice()) {
		const js = JSON.stringify(s).slice(1, -1);
		if (js !== s) parts.push([js, "[redacted]"]);
	}
	return (line) => {
		for (const [s, r] of parts) line = line.split(s).join(r);
		return line;
	};
}

async function turnstileOK(env, token, ip) {
	if (!token || token.length > 2048 || !env.TURNSTILE_SECRET) return false;
	const form = new FormData();
	form.append("secret", env.TURNSTILE_SECRET);
	form.append("response", token);
	if (ip !== "unknown") form.append("remoteip", ip);
	form.append("idempotency_key", crypto.randomUUID());
	const res = await fetch("https://challenges.cloudflare.com/turnstile/v0/siteverify", { method: "POST", body: form });
	const out = await res.json().catch(() => ({}));
	if (!out.success) return false;
	const allowed = String(env.TURNSTILE_HOSTNAMES || "").split(",").map((h) => h.trim()).filter(Boolean);
	return allowed.length === 0 || allowed.includes(out.hostname);
}

async function hmac(key, data) {
	const k = await crypto.subtle.importKey("raw", new TextEncoder().encode(key), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
	const sig = await crypto.subtle.sign("HMAC", k, new TextEncoder().encode(data));
	return btoa(String.fromCharCode(...new Uint8Array(sig))).slice(0, 22);
}

function randomId() {
	const b = crypto.getRandomValues(new Uint8Array(16));
	return btoa(String.fromCharCode(...b)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

// Limiter keeps the abuse limits in one Durable Object in the EU
// jurisdiction. It stores only HMACs of visitor IP addresses (for an hour)
// and portal host names with timestamps (for a day).
export class Limiter extends DurableObject {
	async begin(ipHash, host, isDemo) {
		const now = Date.now();
		const s = (await this.ctx.storage.get("state")) || { ips: {}, hosts: {}, running: {}, day: "", total: 0 };
		const day = new Date(now).toISOString().slice(0, 10);
		if (s.day !== day) {
			s.day = day;
			s.total = 0;
			for (const h of Object.keys(s.hosts)) s.hosts[h].today = 0;
		}
		// Runs that never ended (a crashed Worker) free their slot after 5 minutes.
		for (const [id, r] of Object.entries(s.running)) if (now - r.started > 5 * 60_000) delete s.running[id];
		for (const [ip, list] of Object.entries(s.ips)) {
			s.ips[ip] = list.filter((t) => now - t < HOUR);
			if (s.ips[ip].length === 0) delete s.ips[ip];
		}
		for (const [h, v] of Object.entries(s.hosts)) if (now - v.last > DAY && !Object.values(s.running).some((r) => r.host === h)) delete s.hosts[h];

		const perIP = Number(this.env.PER_IP_PER_HOUR || 5);
		const ipRuns = s.ips[ipHash] || [];
		if (ipRuns.length >= perIP)
			return { ok: false, code: "rate_limited", message: `You can run ${perIP} checks per hour; try again later.`, retryAfter: (ipRuns[0] + HOUR - now) / 1000 };
		if (s.total >= Number(this.env.DAILY_CAP || 200))
			return { ok: false, code: "daily_cap", message: "The checker has reached its daily limit; try again tomorrow.", retryAfter: (Date.parse(day + "T00:00:00Z") + DAY - now) / 1000 };
		const h = s.hosts[host] || { last: 0, today: 0 };
		if (Object.values(s.running).some((r) => r.host === host))
			return { ok: false, code: "busy", message: "This portal is being checked right now; try again in a few minutes.", retryAfter: 180 };
		if (!isDemo) {
			const cooldown = Number(this.env.COOLDOWN_SECONDS || 600) * 1000;
			if (h.last && now - h.last < cooldown)
				return { ok: false, code: "cooldown", message: "This portal was checked a few minutes ago; try again later.", retryAfter: (h.last + cooldown - now) / 1000 };
			if (h.today >= Number(this.env.PER_HOST_PER_DAY || 10))
				return { ok: false, code: "daily_cap", message: "This portal has been checked often today; try again tomorrow.", retryAfter: (Date.parse(day + "T00:00:00Z") + DAY - now) / 1000 };
		}
		const max = Number(this.env.MAX_RUNNING || 2);
		const used = new Set(Object.values(s.running).map((r) => r.slot));
		let slot = -1;
		for (let i = 0; i < max; i++)
			if (!used.has(i)) {
				slot = i;
				break;
			}
		if (slot < 0) return { ok: false, code: "busy", message: "The checker is busy; try again in a few minutes.", retryAfter: 120 };

		const runId = crypto.randomUUID();
		s.running[runId] = { host, slot, started: now };
		s.ips[ipHash] = [...ipRuns, now];
		s.total++;
		h.today++;
		s.hosts[host] = h;
		await this.ctx.storage.put("state", s);
		return { ok: true, slot, runId };
	}

	async end(runId, ran) {
		const s = await this.ctx.storage.get("state");
		if (!s || !s.running[runId]) return;
		const { host } = s.running[runId];
		delete s.running[runId];
		if (ran && s.hosts[host]) s.hosts[host].last = Date.now();
		if (!ran) {
			// Refused before the run: it does not count for the portal.
			if (s.hosts[host]) s.hosts[host].today = Math.max(0, s.hosts[host].today - 1);
			s.total = Math.max(0, s.total - 1);
		}
		await this.ctx.storage.put("state", s);
	}
}
