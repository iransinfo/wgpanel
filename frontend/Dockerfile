FROM node:20-alpine AS build
WORKDIR /src

COPY package*.json ./

# افزایش پایداری و حافظه نود برای بیلد
ENV NODE_OPTIONS="--max-old-space-size=1536"
RUN npm ci --fetch-retries=5 --fetch-retry-mintimeout=20000 --fetch-retry-maxtimeout=120000

COPY . .
RUN npm run build

FROM nginx:1.27-alpine
COPY --from=build /src/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
