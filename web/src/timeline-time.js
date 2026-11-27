let dateFormatter, clockFormatter, monthFormatter, formatterZone;

function useTimezone(timeZone) {
  if (dateFormatter && formatterZone === timeZone) return;
  dateFormatter = new Intl.DateTimeFormat('en-CA', { timeZone, year: 'numeric', month: '2-digit', day: '2-digit' });
  clockFormatter = new Intl.DateTimeFormat('en-GB', { timeZone, hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });
  monthFormatter = new Intl.DateTimeFormat('en-US', { timeZone, month: 'short', day: 'numeric' });
  formatterZone = timeZone;
}

export function serviceDate(timezone, now = Date.now()) {
  useTimezone(timezone);
  const { year, month, day } = Object.fromEntries(dateFormatter.formatToParts(now).map(part => [part.type, part.value]));
  return `${year}-${month}-${day}`;
}

export function nextServiceDay(timezone, now, today = serviceDate(timezone, now)) {
  if (globalThis.Temporal) {
    useTimezone(timezone);
    const local = Temporal.Instant.fromEpochMilliseconds(now).toZonedDateTimeISO(dateFormatter.resolvedOptions().timeZone);
    return local.add({ days: 1 }).startOfDay().epochMilliseconds;
  }
  let before = now, after = now + 36 * 60 * 60 * 1000;
  while (after - before > 1) {
    const middle = Math.floor((before + after) / 2);
    if (serviceDate(timezone, middle) === today) before = middle;
    else after = middle;
  }
  return after;
}

export function timelineTime(value, timezone, today, english) {
  const instant = Date.parse(value);
  if (!Number.isFinite(instant)) return '';
  try {
    const date = serviceDate(timezone, instant);
    if (date === today) return clockFormatter.format(instant);
    const [year, month, day] = date.split('-');
    const sameYear = year === today.slice(0, 4);
    if (english) return sameYear ? monthFormatter.format(instant) : date;
    return `${sameYear ? '' : `${year}年`}${Number(month)}月${Number(day)}日`;
  } catch { return ''; }
}
