import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import en from './en.json'
import fa from './fa.json'
import { mergeTrees, s6Resources } from './s6-resources'

const s6 = s6Resources()

void i18n.use(initReactI18next).init({
  resources: { en: { translation: mergeTrees(en, s6.en) }, fa: { translation: mergeTrees(fa, s6.fa) } },
  lng: 'en',
  fallbackLng: 'en',
  interpolation: { escapeValue: false },
})

export default i18n
