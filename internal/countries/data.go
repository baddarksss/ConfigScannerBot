package countries

import (
	"sort"
	"strings"
)

// Generated from the app's CountryData.java (ISO 3166-1: code, English, Persian).
type Country struct {
	Code string
	EN   string
	FA   string
}

var byCode = map[string]Country{
	"AD": {"AD", "Andorra", "آندورا"},
	"AE": {"AE", "United Arab Emirates", "امارات متحده عربی"},
	"AF": {"AF", "Afghanistan", "افغانستان"},
	"AG": {"AG", "Antigua and Barbuda", "آنتیگوآ و باربودا"},
	"AI": {"AI", "Anguilla", "آنگویلا"},
	"AL": {"AL", "Albania", "آلبانی"},
	"AM": {"AM", "Armenia", "ارمنستان"},
	"AO": {"AO", "Angola", "آنگولا"},
	"AQ": {"AQ", "Antarctica", "قطب جنوب"},
	"AR": {"AR", "Argentina", "آرژانتین"},
	"AS": {"AS", "American Samoa", "ساموآی آمریکایی"},
	"AT": {"AT", "Austria", "اتریش"},
	"AU": {"AU", "Australia", "استرالیا"},
	"AW": {"AW", "Aruba", "آروبا"},
	"AX": {"AX", "Aland Islands", "جزایر آلند"},
	"AZ": {"AZ", "Azerbaijan", "آذربایجان"},
	"BA": {"BA", "Bosnia and Herzegovina", "بوسنی و هرزگوین"},
	"BB": {"BB", "Barbados", "باربادوس"},
	"BD": {"BD", "Bangladesh", "بنگلادش"},
	"BE": {"BE", "Belgium", "بلژیک"},
	"BF": {"BF", "Burkina Faso", "بورکینافاسو"},
	"BG": {"BG", "Bulgaria", "بلغارستان"},
	"BH": {"BH", "Bahrain", "بحرین"},
	"BI": {"BI", "Burundi", "بوروندی"},
	"BJ": {"BJ", "Benin", "بنین"},
	"BL": {"BL", "Saint Barthelemy", "سنت بارتلومی"},
	"BM": {"BM", "Bermuda", "برمودا"},
	"BN": {"BN", "Brunei", "برونئی"},
	"BO": {"BO", "Bolivia", "بولیوی"},
	"BQ": {"BQ", "Caribbean Netherlands", "هلند کارائیبی"},
	"BR": {"BR", "Brazil", "برزیل"},
	"BS": {"BS", "Bahamas", "باهاما"},
	"BT": {"BT", "Bhutan", "بوتان"},
	"BV": {"BV", "Bouvet Island", "جزیره بووه"},
	"BW": {"BW", "Botswana", "بوتسوانا"},
	"BY": {"BY", "Belarus", "بلاروس"},
	"BZ": {"BZ", "Belize", "بلیز"},
	"CA": {"CA", "Canada", "کانادا"},
	"CC": {"CC", "Cocos (Keeling) Islands", "جزایر کوکوس"},
	"CD": {"CD", "DR Congo", "کنگو دموکراتیک"},
	"CF": {"CF", "Central African Republic", "جمهوری آفریقای مرکزی"},
	"CG": {"CG", "Congo", "کنگو"},
	"CH": {"CH", "Switzerland", "سوئیس"},
	"CI": {"CI", "Ivory Coast", "ساحل عاج"},
	"CK": {"CK", "Cook Islands", "جزایر کوک"},
	"CL": {"CL", "Chile", "شیلی"},
	"CM": {"CM", "Cameroon", "کامرون"},
	"CN": {"CN", "China", "چین"},
	"CO": {"CO", "Colombia", "کلمبیا"},
	"CR": {"CR", "Costa Rica", "کاستاریکا"},
	"CU": {"CU", "Cuba", "کوبا"},
	"CV": {"CV", "Cape Verde", "کپ‌ورده"},
	"CW": {"CW", "Curacao", "کوراچائو"},
	"CX": {"CX", "Christmas Island", "جزیره کریسمس"},
	"CY": {"CY", "Cyprus", "قبرص"},
	"CZ": {"CZ", "Czechia", "چک"},
	"DE": {"DE", "Germany", "آلمان"},
	"DJ": {"DJ", "Djibouti", "جیبوتی"},
	"DK": {"DK", "Denmark", "دانمارک"},
	"DM": {"DM", "Dominica", "دومینیکا"},
	"DO": {"DO", "Dominican Republic", "جمهوری دومینیکن"},
	"DZ": {"DZ", "Algeria", "الجزایر"},
	"EC": {"EC", "Ecuador", "اکوادور"},
	"EE": {"EE", "Estonia", "استونی"},
	"EG": {"EG", "Egypt", "مصر"},
	"EH": {"EH", "Western Sahara", "صحرای غربی"},
	"ER": {"ER", "Eritrea", "اریتره"},
	"ES": {"ES", "Spain", "اسپانیا"},
	"ET": {"ET", "Ethiopia", "اتیوپی"},
	"FI": {"FI", "Finland", "فنلاند"},
	"FJ": {"FJ", "Fiji", "فیجی"},
	"FK": {"FK", "Falkland Islands", "جزایر فالکلند"},
	"FM": {"FM", "Micronesia", "میکرونزی"},
	"FO": {"FO", "Faroe Islands", "جزایر فارو"},
	"FR": {"FR", "France", "فرانسه"},
	"GA": {"GA", "Gabon", "گابن"},
	"GB": {"GB", "United Kingdom", "بریتانیا"},
	"GD": {"GD", "Grenada", "گرنادا"},
	"GE": {"GE", "Georgia", "گرجستان"},
	"GF": {"GF", "French Guiana", "گویان فرانسوی"},
	"GG": {"GG", "Guernsey", "گرنزی"},
	"GH": {"GH", "Ghana", "گانا"},
	"GI": {"GI", "Gibraltar", "جبل‌الطارق"},
	"GL": {"GL", "Greenland", "گرینلند"},
	"GM": {"GM", "Gambia", "گامبیا"},
	"GN": {"GN", "Guinea", "گینه"},
	"GP": {"GP", "Guadeloupe", "گوادلوپ"},
	"GQ": {"GQ", "Equatorial Guinea", "گینه‌ی معادلی"},
	"GR": {"GR", "Greece", "یونان"},
	"GS": {"GS", "South Georgia", "جرجیای جنوبی"},
	"GT": {"GT", "Guatemala", "گواتمالا"},
	"GU": {"GU", "Guam", "گوآم"},
	"GW": {"GW", "Guinea-Bissau", "گینه‌بیساو"},
	"GY": {"GY", "Guyana", "گویان"},
	"HK": {"HK", "Hong Kong", "هنگ‌کنگ"},
	"HM": {"HM", "Heard and McDonald Islands", "جزایر هرد و مک‌دونالد"},
	"HN": {"HN", "Honduras", "هندوراس"},
	"HR": {"HR", "Croatia", "کرواسی"},
	"HT": {"HT", "Haiti", "هائیتی"},
	"HU": {"HU", "Hungary", "مجارستان"},
	"ID": {"ID", "Indonesia", "اندونزی"},
	"IE": {"IE", "Ireland", "ایرلند"},
	"IL": {"IL", "Israel", "اسرائیل"},
	"IN": {"IN", "India", "هند"},
	"IO": {"IO", "British Indian Ocean Territory", "قلمرو اقیانوس هند بریتانیا"},
	"IQ": {"IQ", "Iraq", "عراق"},
	"IR": {"IR", "Iran", "ایران"},
	"IS": {"IS", "Iceland", "ایسلند"},
	"IT": {"IT", "Italy", "ایتالیا"},
	"JE": {"JE", "Jersey", "جِرزی"},
	"JM": {"JM", "Jamaica", "جامائیکا"},
	"JO": {"JO", "Jordan", "اردن"},
	"JP": {"JP", "Japan", "ژاپن"},
	"KE": {"KE", "Kenya", "کنیا"},
	"KG": {"KG", "Kyrgyzstan", "قرقیزستان"},
	"KH": {"KH", "Cambodia", "کمبوجا"},
	"KI": {"KI", "Kiribati", "کیریباتی"},
	"KM": {"KM", "Comoros", "کومور"},
	"KN": {"KN", "Saint Kitts and Nevis", "سنت کیتس و نویس"},
	"KP": {"KP", "North Korea", "کره شمالی"},
	"KR": {"KR", "South Korea", "کره جنوبی"},
	"KW": {"KW", "Kuwait", "کویت"},
	"KZ": {"KZ", "Kazakhstan", "قزاقستان"},
	"LA": {"LA", "Laos", "لائوس"},
	"LB": {"LB", "Lebanon", "لبنان"},
	"LC": {"LC", "Saint Lucia", "سنت لوسیا"},
	"LI": {"LI", "Liechtenstein", "لیختن‌اشتاین"},
	"LK": {"LK", "Sri Lanka", "سری‌لانکا"},
	"LR": {"LR", "Liberia", "لیبریا"},
	"LS": {"LS", "Lesotho", "لسوتو"},
	"LT": {"LT", "Lithuania", "لیتوانی"},
	"LU": {"LU", "Luxembourg", "لوکسبورگ"},
	"LV": {"LV", "Latvia", "لتونی"},
	"LY": {"LY", "Libya", "لیبی"},
	"MA": {"MA", "Morocco", "مراکش"},
	"MC": {"MC", "Monaco", "موناکو"},
	"MD": {"MD", "Moldova", "مولداوی"},
	"ME": {"ME", "Montenegro", "مونته‌نگرو"},
	"MF": {"MF", "Saint Martin", "سنت مارتین"},
	"MG": {"MG", "Madagascar", "ماداگاسکار"},
	"MH": {"MH", "Marshall Islands", "جزایر مارشال"},
	"MK": {"MK", "North Macedonia", "مقدونیه شمالی"},
	"ML": {"ML", "Mali", "مالی"},
	"MM": {"MM", "Myanmar", "میانمار"},
	"MN": {"MN", "Mongolia", "مغولستان"},
	"MO": {"MO", "Macao", "ماکائو"},
	"MP": {"MP", "Northern Mariana Islands", "جزایر ماریانای شمالی"},
	"MQ": {"MQ", "Martinique", "مارتینیک"},
	"MR": {"MR", "Mauritania", "موریتانی"},
	"MS": {"MS", "Montserrat", "مونتسرات"},
	"MT": {"MT", "Malta", "مالت"},
	"MU": {"MU", "Mauritius", "موریس"},
	"MV": {"MV", "Maldives", "مالدیو"},
	"MW": {"MW", "Malawi", "مالاوی"},
	"MX": {"MX", "Mexico", "مکزیک"},
	"MY": {"MY", "Malaysia", "مالزی"},
	"MZ": {"MZ", "Mozambique", "موزامبیک"},
	"NA": {"NA", "Namibia", "نامیبیا"},
	"NC": {"NC", "New Caledonia", "نیوکالدونی"},
	"NE": {"NE", "Niger", "نیجر"},
	"NF": {"NF", "Norfolk Island", "جزیره نورفک"},
	"NG": {"NG", "Nigeria", "نیجریه"},
	"NI": {"NI", "Nicaragua", "نیکاراگوئه"},
	"NL": {"NL", "Netherlands", "هلند"},
	"NO": {"NO", "Norway", "نروژ"},
	"NP": {"NP", "Nepal", "نپال"},
	"NR": {"NR", "Nauru", "نائورو"},
	"NU": {"NU", "Niue", "نیووی"},
	"NZ": {"NZ", "New Zealand", "نیوزیلند"},
	"OM": {"OM", "Oman", "عمان"},
	"PA": {"PA", "Panama", "پاناما"},
	"PE": {"PE", "Peru", "پرو"},
	"PF": {"PF", "French Polynesia", "پلی‌نزی فرانسه"},
	"PG": {"PG", "Papua New Guinea", "پاپوآ گینه نو"},
	"PH": {"PH", "Philippines", "فیلیپین"},
	"PK": {"PK", "Pakistan", "پاکستان"},
	"PL": {"PL", "Poland", "لهستان"},
	"PM": {"PM", "Saint Pierre and Miquelon", "سنت پیر و میکلون"},
	"PN": {"PN", "Pitcairn Islands", "جزایر پیتکرن"},
	"PR": {"PR", "Puerto Rico", "پورتو ریکو"},
	"PS": {"PS", "Palestine", "فلسطین"},
	"PT": {"PT", "Portugal", "پرتغال"},
	"PW": {"PW", "Palau", "پالائو"},
	"PY": {"PY", "Paraguay", "پاراگوئه"},
	"QA": {"QA", "Qatar", "قطر"},
	"RE": {"RE", "Reunion", "رئونیون"},
	"RO": {"RO", "Romania", "رومانی"},
	"RS": {"RS", "Serbia", "صربستان"},
	"RU": {"RU", "Russia", "روسیه"},
	"RW": {"RW", "Rwanda", "رواندا"},
	"SA": {"SA", "Saudi Arabia", "عربستان سعودی"},
	"SB": {"SB", "Solomon Islands", "جزایر سلیمان"},
	"SC": {"SC", "Seychelles", "سیشل"},
	"SD": {"SD", "Sudan", "سودان"},
	"SE": {"SE", "Sweden", "سوئد"},
	"SG": {"SG", "Singapore", "سنگاپور"},
	"SH": {"SH", "Saint Helena", "سنت هلنا"},
	"SI": {"SI", "Slovenia", "اسلوونی"},
	"SJ": {"SJ", "Svalbard and Jan Mayen", "سوالبارد"},
	"SK": {"SK", "Slovakia", "اسلواکی"},
	"SL": {"SL", "Sierra Leone", "سیرالئون"},
	"SM": {"SM", "San Marino", "سان‌مارینو"},
	"SN": {"SN", "Senegal", "سنگال"},
	"SO": {"SO", "Somalia", "سومالی"},
	"SR": {"SR", "Suriname", "سورینام"},
	"SS": {"SS", "South Sudan", "سودان جنوبی"},
	"ST": {"ST", "Sao Tome and Principe", "سائوتومه و پرنسیپ"},
	"SV": {"SV", "El Salvador", "السالوادور"},
	"SX": {"SX", "Sint Maarten", "سینت مارتن"},
	"SY": {"SY", "Syria", "سوریه"},
	"SZ": {"SZ", "Eswatini", "اسواتینی"},
	"TC": {"TC", "Turks and Caicos Islands", "جزایر تورکس و کائیکوس"},
	"TD": {"TD", "Chad", "چاد"},
	"TF": {"TF", "French Southern Lands", "سرزمین‌های جنوبی فرانسه"},
	"TG": {"TG", "Togo", "توگو"},
	"TH": {"TH", "Thailand", "تایلند"},
	"TJ": {"TJ", "Tajikistan", "تاجیکستان"},
	"TK": {"TK", "Tokelau", "توکلائو"},
	"TL": {"TL", "Timor-Leste", "تیمور شرقی"},
	"TM": {"TM", "Turkmenistan", "ترکمنستان"},
	"TN": {"TN", "Tunisia", "تونس"},
	"TO": {"TO", "Tonga", "تونگا"},
	"TR": {"TR", "Turkey", "ترکیه"},
	"TT": {"TT", "Trinidad and Tobago", "ترینیداد و توباگو"},
	"TV": {"TV", "Tuvalu", "توالو"},
	"TW": {"TW", "Taiwan", "تایوان"},
	"TZ": {"TZ", "Tanzania", "تانزانیا"},
	"UA": {"UA", "Ukraine", "اوکراین"},
	"UG": {"UG", "Uganda", "اوگاندا"},
	"UM": {"UM", "US Minor Outlying Islands", "جزایر کوچک متفرقه آمریکا"},
	"US": {"US", "United States", "ایالات متحده"},
	"UY": {"UY", "Uruguay", "اروگوئه"},
	"UZ": {"UZ", "Uzbekistan", "ازبکستان"},
	"VA": {"VA", "Vatican City", "واتیکان"},
	"VC": {"VC", "Saint Vincent and the Grenadines", "سنت وینسنت و گرنادین"},
	"VE": {"VE", "Venezuela", "ونزوئلا"},
	"VG": {"VG", "British Virgin Islands", "جزایر ویرجین بریتانیا"},
	"VI": {"VI", "US Virgin Islands", "جزایر ویرجین آمریکا"},
	"VN": {"VN", "Vietnam", "ویتنام"},
	"VU": {"VU", "Vanuatu", "وانواتو"},
	"WF": {"WF", "Wallis and Futuna", "والیس و فوتونا"},
	"WS": {"WS", "Samoa", "ساموآ"},
	"XK": {"XK", "Kosovo", "کوزوو"},
	"YE": {"YE", "Yemen", "یمن"},
	"YT": {"YT", "Mayotte", "مایوت"},
	"ZA": {"ZA", "South Africa", "آفریقای جنوبی"},
	"ZM": {"ZM", "Zambia", "زامبیا"},
	"ZW": {"ZW", "Zimbabwe", "زیمبابوه"},
}

// Names returns the country name in the given language ("fa" or "en").
func Names(code, lang string) (string, bool) {
	c, ok := byCode[strings.ToUpper(code)]
	if !ok {
		return "", false
	}
	if lang == "fa" {
		return c.FA, true
	}
	return c.EN, true
}

// Known reports whether the ISO code exists in the table.
func Known(code string) bool { _, ok := byCode[strings.ToUpper(code)]; return ok }

// CodeByName finds the ISO 3166-1 alpha-2 code for a country by its English or Persian name.
func CodeByName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	lower := strings.ToLower(name)
	for code, c := range byCode {
		if strings.EqualFold(c.EN, name) || strings.EqualFold(c.FA, name) ||
			strings.ToLower(c.EN) == lower || strings.ToLower(c.FA) == lower {
			return code
		}
	}
	for code, c := range byCode {
		if strings.Contains(strings.ToLower(c.EN), lower) || strings.Contains(strings.ToLower(c.FA), lower) {
			return code
		}
	}
	return ""
}

// All returns every known country in a stable (code) order — used for
// exporting emoji codes in the same order as the app.
func All() []Country {
	out := make([]Country, 0, len(byCode))
	for _, c := range byCode {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}
