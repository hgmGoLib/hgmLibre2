// cre2_internal.h — cre2 各 .cpp 之间共用的【内部】句柄定义。
// 不对外: cre2.h 里这些类型只留不完整声明 (typedef struct cre2_set cre2_set;),
// 调用方只拿指针。放这里是为了多个 .cpp 能共用同一份定义而不必互相 #include .cpp。
#ifndef CRE2_INTERNAL_H
#define CRE2_INTERNAL_H

#include "re2_re2.h"
#include "re2_set.h"

struct cre2_set {
	re2::RE2::Set *set;
};

#endif // CRE2_INTERNAL_H
