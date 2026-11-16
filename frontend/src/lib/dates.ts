// The date of a fact as the API gives it: the date of the statement or act
// when the fact has one, with how much of it is known (`precision`: a 2013
// statement recalled by a 2026 article is "2013", not a day), else the date
// of its first source, a full date with no precision.
export type DatePrecision = 'year' | 'month' | 'day';

// statementDateText prints a statement date as precisely as it is known ("2013",
// "mars 2024", "14 mars 2024"); '' for a source's date, which the caller
// prints its own way.
export function statementDateText(iso: string | null | undefined, precision: string | null | undefined, lang: string): string {
	if (!iso || !precision) return '';
	const d = new Date(iso);
	const locale = lang === 'en' ? 'en-GB' : 'fr-FR';
	if (precision === 'year') return String(d.getUTCFullYear());
	if (precision === 'month') return d.toLocaleDateString(locale, { month: 'long', year: 'numeric', timeZone: 'UTC' });
	return d.toLocaleDateString(locale, { day: 'numeric', month: 'long', year: 'numeric', timeZone: 'UTC' });
}

// factDateText prints the date of a fact as precisely as it is known: a
// statement date at its precision, a source's date as its full day.
export function factDateText(iso: string | null | undefined, precision: string | null | undefined, lang: string): string {
	if (!iso) return '';
	if (precision) return statementDateText(iso, precision, lang);
	return new Date(iso).toLocaleDateString(lang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'long', year: 'numeric' });
}
